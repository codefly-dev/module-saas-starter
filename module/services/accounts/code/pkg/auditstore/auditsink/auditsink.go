// Package auditsink reads the audit sink settings and opens what they name: the
// store of record and the locked archive of a swap value (ADR 0009). The
// accounts service and the one-time history copy both read the same
// settings through it, so a deployment configures the swap once.
package auditsink

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"accounts/pkg/auditstore/bigquerystore"
	"accounts/pkg/auditstore/clickhousestore"
	"accounts/pkg/auditstore/gcsarchive"
	"accounts/pkg/auditstore/s3archive"
	"accounts/pkg/business"

	"cloud.google.com/go/bigquery"
	bqstorage "cloud.google.com/go/bigquery/storage/apiv1"
	"cloud.google.com/go/storage"
	"github.com/ClickHouse/clickhouse-go/v2"
)

// The audit sink settings. AUDIT_SINK selects the mode (business.AuditSinkMode);
// each mode's settings are required only under it, and a missing one fails
// startup rather than degrading the audit trail.
const (
	EnvSink = "AUDIT_SINK"

	// AUDIT_SINK=both (ADR 0006).
	EnvExternalURL   = "AUDIT_EXTERNAL_URL"
	EnvExternalToken = "AUDIT_EXTERNAL_TOKEN"

	// AUDIT_SINK=bigquery (ADR 0009). Credentials are Application Default
	// Credentials only — on Kubernetes, the pod's workload identity — so there
	// is deliberately no key-file or token setting.
	EnvBigQueryProject = "AUDIT_BIGQUERY_PROJECT"
	EnvBigQueryDataset = "AUDIT_BIGQUERY_DATASET"

	// AUDIT_SINK=clickhouse (ADR 0009). The DSN names the server, the database
	// and the credentials, and comes from the deployment's secret: it has no
	// default and is never logged or echoed in an error. The cluster, when set,
	// makes the tables replicated across it. The events retention is the
	// compliance window, the events table's TTL; BigQuery's events table does
	// not expire, so AUDIT_SINK=bigquery does not read it.
	EnvClickHouseDSN       = "AUDIT_CLICKHOUSE_DSN"
	EnvClickHouseCluster   = "AUDIT_CLICKHOUSE_CLUSTER"
	EnvEventsRetentionDays = "AUDIT_EVENTS_RETENTION_DAYS"

	// Every swap value.
	EnvArchiveURL           = "AUDIT_ARCHIVE_URL"
	EnvDeploymentID         = "AUDIT_DEPLOYMENT_ID"
	EnvContentRetentionDays = "AUDIT_CONTENT_RETENTION_DAYS"
	// Optional relay tuning.
	EnvRelayBatchSize = "AUDIT_RELAY_BATCH_SIZE"
	EnvRelayMaxWait   = "AUDIT_RELAY_MAX_WAIT"
)

// Config is the resolved audit sink: the mode and, for the modes that write
// outside Postgres, where.
type Config struct {
	Mode business.AuditSinkMode
	// External is the tee's destination under AUDIT_SINK=both.
	External business.ExternalAuditSink
	// Swap is the store of record and archive under a swap value.
	Swap *Swap
}

// Swap is a validated swap-value configuration.
type Swap struct {
	Mode business.AuditSinkMode
	// BigQuery names the store of record under AUDIT_SINK=bigquery.
	BigQuery *BigQuery
	// ClickHouse names the store of record under AUDIT_SINK=clickhouse.
	ClickHouse *ClickHouse
	Archive    ArchiveLocation
	// DeploymentID stamps every record and confines every read.
	DeploymentID string
	// ContentRetention is how long a content-class event's details are kept.
	ContentRetention time.Duration
	RelayBatchSize   int
	RelayMaxWait     time.Duration
}

// BigQuery is where the BigQuery store of record lives.
type BigQuery struct {
	Project string
	Dataset string
}

// ClickHouse is where the ClickHouse store of record lives.
type ClickHouse struct {
	// DSN carries the credentials: it is passed to the driver and nowhere else.
	DSN string
	// Database is the database the DSN names.
	Database string
	// Cluster, when set, creates the tables ON CLUSTER, replicated.
	Cluster string
	// EventsRetention is the events table's TTL: the compliance window.
	EventsRetention time.Duration
}

// String names the store without its DSN, so a ClickHouse printed by mistake
// never shows the credentials.
func (c ClickHouse) String() string {
	return fmt.Sprintf("clickhouse database %q (cluster %q)", c.Database, c.Cluster)
}

// GoString is String: %#v of a Swap prints its fields this way.
func (c ClickHouse) GoString() string { return c.String() }

// ArchiveLocation is a parsed AUDIT_ARCHIVE_URL. The scheme picks the archive
// writer: gs (Google Cloud Storage) or s3 (Amazon S3, or an S3-compatible
// store). Any warehouse pairs with any archive.
type ArchiveLocation struct {
	Scheme string
	Bucket string
}

// The AUDIT_ARCHIVE_URL schemes with a writer.
const (
	ArchiveSchemeGCS = "gs"
	ArchiveSchemeS3  = "s3"
)

// Load reads AUDIT_SINK and the settings its mode requires through getenv
// (os.Getenv in production). "postgres" (the default) keeps the durable
// emitter alone. "both" adds an asynchronous tee to an HTTP endpoint while
// audit_events stays the store of record. "bigquery" and "clickhouse" swap the
// store of record to that warehouse, with Postgres keeping only the
// transactional queue. "external" is refused: no destination outside Postgres
// can join the transaction a change commits in.
func Load(getenv func(string) string) (Config, error) {
	mode, err := Mode(getenv)
	if err != nil {
		return Config{}, err
	}
	config := Config{Mode: mode}
	switch {
	case mode == business.AuditSinkBoth:
		config.External, err = loadExternal(getenv)
	case mode.Swaps():
		config.Swap, err = loadSwap(mode, getenv)
	}
	if err != nil {
		return Config{}, err
	}
	return config, nil
}

// Mode reads AUDIT_SINK alone: the one place the setting is read, which Load and
// every process that must agree with the service go through. An unset value is
// the default, postgres, as it is for the service.
func Mode(getenv func(string) string) (business.AuditSinkMode, error) {
	return business.ParseAuditSinkMode(getenv(EnvSink))
}

// RequireMode is Mode for a process that writes audit events apart from the
// service — the role catalog import, run as a deploy step — where an unset
// AUDIT_SINK is not the default but a lost setting. The service's value reaches
// such a process only because the deployment passes it on; when that does not
// happen, defaulting to postgres would write its events to audit_events on a
// deployment whose store of record is a warehouse, where nothing reads them. So
// it refuses, and says how to set it. lookup reports whether the variable is
// set at all (os.LookupEnv); a blank value is as good as none.
func RequireMode(lookup func(string) (string, bool)) (business.AuditSinkMode, error) {
	raw, set := lookup(EnvSink)
	if !set || strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("%s is not set. This process records audit events, and where they go depends on it: "+
			"under bigquery or clickhouse they must reach the queue the audit relay delivers, not audit_events. "+
			"Pass the deployment's value (postgres, both, bigquery or clickhouse); it will not guess", EnvSink)
	}
	return Mode(func(string) string { return raw })
}

func loadExternal(getenv func(string) string) (business.ExternalAuditSink, error) {
	endpoint := strings.TrimSpace(getenv(EnvExternalURL))
	if endpoint == "" {
		return nil, fmt.Errorf("AUDIT_SINK=both requires %s for the external audit destination", EnvExternalURL)
	}
	return business.NewHTTPAuditSink(business.HTTPAuditSinkConfig{
		Endpoint: endpoint,
		Token:    strings.TrimSpace(getenv(EnvExternalToken)),
	})
}

// Bucket naming rules, loosely. GCS: lowercase letters, digits, '-', '_' and
// '.', starting and ending alphanumeric. S3: lowercase letters, digits, '-'
// and '.', 3 to 63 of them, starting and ending alphanumeric.
var bucketNamePatterns = map[string]*regexp.Regexp{
	ArchiveSchemeGCS: regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$`),
	ArchiveSchemeS3:  regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`),
}

// ParseArchiveURL reads AUDIT_ARCHIVE_URL: gs://<bucket> or s3://<bucket>,
// nothing more. Another scheme (an Azure container, say) names an archive no
// writer exists for yet, which is refused at startup rather than left to fail
// at the first delivery.
func ParseArchiveURL(raw string) (ArchiveLocation, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return ArchiveLocation{}, fmt.Errorf("%s must be a URL like gs://<bucket> or s3://<bucket>; got %q", EnvArchiveURL, raw)
	}
	pattern, supported := bucketNamePatterns[parsed.Scheme]
	if !supported {
		return ArchiveLocation{}, fmt.Errorf("%s scheme %q has no archive writer; the supported schemes are %s:// and %s:// (got %q)",
			EnvArchiveURL, parsed.Scheme, ArchiveSchemeGCS, ArchiveSchemeS3, raw)
	}
	if parsed.User != nil || parsed.Port() != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ArchiveLocation{}, fmt.Errorf("%s names a bucket and nothing else, like %s://<bucket>; got %q", EnvArchiveURL, parsed.Scheme, raw)
	}
	if !pattern.MatchString(parsed.Host) {
		return ArchiveLocation{}, fmt.Errorf("%s bucket %q is not a valid bucket name", EnvArchiveURL, parsed.Host)
	}
	return ArchiveLocation{Scheme: parsed.Scheme, Bucket: parsed.Host}, nil
}

// deploymentIDPattern keeps the deployment id usable as an archive path segment
// and a warehouse value without escaping.
var deploymentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// setting is one required setting of a swap value and what it was set to.
type setting struct{ name, value string }

// loadSwap reads and validates every setting of a swap value, reporting all
// the missing ones at once.
func loadSwap(mode business.AuditSinkMode, getenv func(string) string) (*Swap, error) {
	swap := &Swap{
		Mode:           mode,
		DeploymentID:   strings.TrimSpace(getenv(EnvDeploymentID)),
		RelayBatchSize: business.DefaultAuditRelayBatchSize,
		RelayMaxWait:   business.DefaultAuditRelayMaxWait,
	}
	archiveURL := strings.TrimSpace(getenv(EnvArchiveURL))
	retentionDays := strings.TrimSpace(getenv(EnvContentRetentionDays))
	shared := []setting{
		{EnvArchiveURL, archiveURL},
		{EnvDeploymentID, swap.DeploymentID},
		{EnvContentRetentionDays, retentionDays},
	}
	var required []setting
	var eventsRetentionDays string
	switch mode {
	case business.AuditSinkBigQuery:
		swap.BigQuery = &BigQuery{Project: strings.TrimSpace(getenv(EnvBigQueryProject)), Dataset: strings.TrimSpace(getenv(EnvBigQueryDataset))}
		required = append([]setting{
			{EnvBigQueryProject, swap.BigQuery.Project},
			{EnvBigQueryDataset, swap.BigQuery.Dataset},
		}, shared...)
	case business.AuditSinkClickHouse:
		swap.ClickHouse = &ClickHouse{DSN: strings.TrimSpace(getenv(EnvClickHouseDSN)), Cluster: strings.TrimSpace(getenv(EnvClickHouseCluster))}
		eventsRetentionDays = strings.TrimSpace(getenv(EnvEventsRetentionDays))
		required = append(append([]setting{
			{EnvClickHouseDSN, swap.ClickHouse.DSN},
		}, shared...), setting{EnvEventsRetentionDays, eventsRetentionDays})
	default:
		return nil, fmt.Errorf("audit sink: %s is not a swap value", mode)
	}
	var missing []string
	for _, setting := range required {
		if setting.value == "" {
			missing = append(missing, setting.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("AUDIT_SINK=%s requires %s", mode, strings.Join(missing, ", "))
	}

	var err error
	if swap.ContentRetention, err = retentionDuration(EnvContentRetentionDays, retentionDays); err != nil {
		return nil, err
	}
	if swap.ClickHouse != nil {
		if err := loadClickHouse(swap, eventsRetentionDays); err != nil {
			return nil, err
		}
	}
	if !deploymentIDPattern.MatchString(swap.DeploymentID) {
		return nil, fmt.Errorf("%s must be letters, digits, '.', '_' or '-' (at most 128, starting with a letter or digit); got %q",
			EnvDeploymentID, swap.DeploymentID)
	}
	if swap.Archive, err = ParseArchiveURL(archiveURL); err != nil {
		return nil, err
	}
	if raw := strings.TrimSpace(getenv(EnvRelayBatchSize)); raw != "" {
		size, err := strconv.Atoi(raw)
		if err != nil || size < 1 || size > business.MaxAuditRelayBatchSize {
			return nil, fmt.Errorf("%s must be between 1 and %d; got %q", EnvRelayBatchSize, business.MaxAuditRelayBatchSize, raw)
		}
		swap.RelayBatchSize = size
	}
	if raw := strings.TrimSpace(getenv(EnvRelayMaxWait)); raw != "" {
		wait, err := time.ParseDuration(raw)
		if err != nil || wait <= 0 || wait > business.MaxAuditRelayMaxWait {
			return nil, fmt.Errorf("%s must be a positive duration of at most %s, like 5s; got %q", EnvRelayMaxWait, business.MaxAuditRelayMaxWait, raw)
		}
		swap.RelayMaxWait = wait
	}
	return swap, nil
}

// maxRetentionDays bounds a retention setting at a century. A duration counts
// nanoseconds in an int64, which overflows to a negative value past about
// 106,751 days, and a retention that long is a mistake in the setting.
const maxRetentionDays = 36500

// retentionDuration reads a retention setting in whole days, bounding the count
// before it is multiplied into a duration.
func retentionDuration(name, raw string) (time.Duration, error) {
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > maxRetentionDays {
		return 0, fmt.Errorf("%s must be a whole number of days, at least 1 and at most %d; got %q", name, maxRetentionDays, raw)
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

// loadClickHouse validates the ClickHouse settings: a DSN the driver parses
// and that names its database, a cluster name ON CLUSTER can take, and an
// events window at least as long as the content window — content details
// outliving their event would be details of an event the store no longer has.
func loadClickHouse(swap *Swap, eventsRetentionDays string) error {
	settings := swap.ClickHouse
	retention, err := retentionDuration(EnvEventsRetentionDays, eventsRetentionDays)
	if err != nil {
		return err
	}
	settings.EventsRetention = retention
	if settings.EventsRetention < swap.ContentRetention {
		return fmt.Errorf("%s (%d) must be at least %s (%d): content details are kept no longer than their events",
			EnvEventsRetentionDays, int(retention/(24*time.Hour)), EnvContentRetentionDays, int(swap.ContentRetention/(24*time.Hour)))
	}
	options, err := clickhouse.ParseDSN(settings.DSN)
	if err != nil {
		// The parse error can quote the DSN, credentials included, so none of
		// it is reported.
		return fmt.Errorf("%s is not a valid ClickHouse DSN; it looks like clickhouse://<user>:<password>@<host>:9000/<database>", EnvClickHouseDSN)
	}
	if options.Auth.Database == "" {
		return fmt.Errorf("%s must name the database, like clickhouse://<host>:9000/<database>", EnvClickHouseDSN)
	}
	settings.Database = options.Auth.Database
	if settings.Cluster != "" && !clickhousestore.ValidCluster(settings.Cluster) {
		return fmt.Errorf("%s must be a cluster name (letters, digits, '_', '.', '-') or a macro like {cluster}; got %q",
			EnvClickHouseCluster, settings.Cluster)
	}
	return nil
}

// Opened is a swap value's store of record and archive, built and checked.
type Opened struct {
	// Store is the store of record: the relay appends to it and the service
	// reads it.
	Store business.AuditStore
	// History reads back a window of what the store holds, which the history
	// copy verifies against.
	History business.AuditHistoryReader
	// Archive is the locked archive.
	Archive business.AuditArchive
	closers []func()
}

// Close releases every client Open built.
func (o *Opened) Close() {
	for i := len(o.closers) - 1; i >= 0; i-- {
		o.closers[i]()
	}
	o.closers = nil
}

// Open builds the store of record of a swap value — its tables created if
// missing — and the archive its URL's scheme names. BigQuery and GCS clients
// use Application Default Credentials; ClickHouse connects with the DSN; the
// S3 client takes the AWS SDK's default chain.
func Open(ctx context.Context, swap *Swap) (*Opened, error) {
	if swap == nil {
		return nil, errors.New("audit sink: no swap value to open")
	}
	opened := &Opened{}
	fail := func(err error) (*Opened, error) {
		opened.Close()
		return nil, err
	}
	var err error
	switch {
	case swap.Mode == business.AuditSinkBigQuery && swap.BigQuery != nil:
		err = opened.openBigQuery(ctx, swap)
	case swap.Mode == business.AuditSinkClickHouse && swap.ClickHouse != nil:
		err = opened.openClickHouse(ctx, swap)
	default:
		err = fmt.Errorf("audit sink: no store of record for AUDIT_SINK=%s", swap.Mode)
	}
	if err != nil {
		return fail(err)
	}
	if err := opened.openArchive(ctx, swap.Archive); err != nil {
		return fail(err)
	}
	return opened, nil
}

// ensureTimeout bounds creating and checking the store's tables at startup.
const ensureTimeout = time.Minute

// openBigQuery builds the BigQuery store: streaming inserts for the relay, and
// reads on the Storage Read API.
func (opened *Opened) openBigQuery(ctx context.Context, swap *Swap) error {
	bigQueryClient, err := bigquery.NewClient(ctx, swap.BigQuery.Project)
	if err != nil {
		return fmt.Errorf("audit store: bigquery client: %w", err)
	}
	opened.closers = append(opened.closers, func() { _ = bigQueryClient.Close() })
	// Reads run no query job: the service holds the append grant, and append
	// plus job creation would let it run DML against the store of record.
	readClient, err := bqstorage.NewBigQueryReadClient(ctx)
	if err != nil {
		return fmt.Errorf("audit store: bigquery storage read client: %w", err)
	}
	opened.closers = append(opened.closers, func() { _ = readClient.Close() })
	reader, err := bigquerystore.NewReader(bigquerystore.ReadConfig{
		Client:       readClient,
		Project:      swap.BigQuery.Project,
		Dataset:      swap.BigQuery.Dataset,
		DeploymentID: swap.DeploymentID,
	})
	if err != nil {
		return err
	}
	warehouse, err := bigquerystore.New(bigQueryClient, bigquerystore.Config{
		Dataset:                swap.BigQuery.Dataset,
		ContentDetailRetention: swap.ContentRetention,
		Reader:                 reader,
	})
	if err != nil {
		return err
	}
	ensureCtx, cancel := context.WithTimeout(ctx, ensureTimeout)
	err = warehouse.Ensure(ensureCtx)
	cancel()
	if err != nil {
		return err
	}
	opened.Store, opened.History = warehouse, warehouse
	return nil
}

// openClickHouse connects with the DSN and builds the ClickHouse store, which
// writes and reads over that one connection pool.
func (opened *Opened) openClickHouse(ctx context.Context, swap *Swap) error {
	options, err := clickhouse.ParseDSN(swap.ClickHouse.DSN)
	if err != nil {
		// Load has parsed it already; never echo it.
		return fmt.Errorf("audit store: %s is not a valid ClickHouse DSN", EnvClickHouseDSN)
	}
	conn, err := clickhouse.Open(options)
	if err != nil {
		return fmt.Errorf("audit store: clickhouse connection: %w", err)
	}
	opened.closers = append(opened.closers, func() { _ = conn.Close() })
	warehouse, err := clickhousestore.New(conn, clickhousestore.Config{
		Database:               swap.ClickHouse.Database,
		Cluster:                swap.ClickHouse.Cluster,
		DeploymentID:           swap.DeploymentID,
		EventsRetention:        swap.ClickHouse.EventsRetention,
		ContentDetailRetention: swap.ContentRetention,
	})
	if err != nil {
		return err
	}
	ensureCtx, cancel := context.WithTimeout(ctx, ensureTimeout)
	err = warehouse.Ensure(ensureCtx)
	cancel()
	if err != nil {
		return err
	}
	opened.Store, opened.History = warehouse, warehouse
	return nil
}

// openArchive builds the writer the archive URL's scheme names.
func (opened *Opened) openArchive(ctx context.Context, location ArchiveLocation) error {
	var err error
	switch location.Scheme {
	case ArchiveSchemeGCS:
		storageClient, clientErr := storage.NewClient(ctx)
		if clientErr != nil {
			return fmt.Errorf("audit archive: storage client: %w", clientErr)
		}
		opened.closers = append(opened.closers, func() { _ = storageClient.Close() })
		opened.Archive, err = gcsarchive.New(storageClient, location.Bucket)
	case ArchiveSchemeS3:
		opened.Archive, err = s3archive.NewFromEnvironment(ctx, location.Bucket)
	default:
		err = fmt.Errorf("audit archive: no writer for scheme %q", location.Scheme)
	}
	return err
}
