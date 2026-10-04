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
	"accounts/pkg/auditstore/gcsarchive"
	"accounts/pkg/business"

	"cloud.google.com/go/bigquery"
	bqstorage "cloud.google.com/go/bigquery/storage/apiv1"
	"cloud.google.com/go/storage"
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
	EnvBigQueryProject      = "AUDIT_BIGQUERY_PROJECT"
	EnvBigQueryDataset      = "AUDIT_BIGQUERY_DATASET"
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
	Archive  ArchiveLocation
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

// ArchiveLocation is a parsed AUDIT_ARCHIVE_URL. The scheme picks the archive
// writer; gs (Google Cloud Storage) is the one built.
type ArchiveLocation struct {
	Scheme string
	Bucket string
}

// ArchiveSchemeGCS is the AUDIT_ARCHIVE_URL scheme of a GCS bucket.
const ArchiveSchemeGCS = "gs"

// Load reads AUDIT_SINK and the settings its mode requires through getenv
// (os.Getenv in production). "postgres" (the default) keeps the durable
// emitter alone. "both" adds an asynchronous tee to an HTTP endpoint while
// audit_events stays the store of record. "bigquery" swaps the store of record
// to BigQuery, with Postgres keeping only the transactional queue. "external"
// is refused: no destination outside Postgres can join the transaction a
// change commits in.
func Load(getenv func(string) string) (Config, error) {
	mode, err := business.ParseAuditSinkMode(getenv(EnvSink))
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

// gcsBucketNamePattern is GCS's bucket naming rule, loosely: lowercase
// letters, digits, '-', '_' and '.', starting and ending alphanumeric.
var gcsBucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$`)

// ParseArchiveURL reads AUDIT_ARCHIVE_URL: gs://<bucket>, nothing more.
// Another scheme names an archive no writer exists for yet, which is refused
// at startup rather than left to fail at the first delivery.
func ParseArchiveURL(raw string) (ArchiveLocation, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return ArchiveLocation{}, fmt.Errorf("%s must be a URL like gs://<bucket>; got %q", EnvArchiveURL, raw)
	}
	if parsed.Scheme != ArchiveSchemeGCS {
		return ArchiveLocation{}, fmt.Errorf("%s scheme %q has no archive writer; the supported scheme is %s:// (got %q)",
			EnvArchiveURL, parsed.Scheme, ArchiveSchemeGCS, raw)
	}
	if parsed.User != nil || parsed.Port() != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ArchiveLocation{}, fmt.Errorf("%s names a bucket and nothing else, like gs://<bucket>; got %q", EnvArchiveURL, raw)
	}
	if !gcsBucketNamePattern.MatchString(parsed.Host) {
		return ArchiveLocation{}, fmt.Errorf("%s bucket %q is not a valid bucket name", EnvArchiveURL, parsed.Host)
	}
	return ArchiveLocation{Scheme: parsed.Scheme, Bucket: parsed.Host}, nil
}

// deploymentIDPattern keeps the deployment id usable as an archive path segment
// and a warehouse value without escaping.
var deploymentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// loadSwap reads and validates every setting of a swap value, reporting all
// the missing ones at once.
func loadSwap(mode business.AuditSinkMode, getenv func(string) string) (*Swap, error) {
	swap := &Swap{
		Mode:           mode,
		BigQuery:       &BigQuery{Project: strings.TrimSpace(getenv(EnvBigQueryProject)), Dataset: strings.TrimSpace(getenv(EnvBigQueryDataset))},
		DeploymentID:   strings.TrimSpace(getenv(EnvDeploymentID)),
		RelayBatchSize: business.DefaultAuditRelayBatchSize,
		RelayMaxWait:   business.DefaultAuditRelayMaxWait,
	}
	archiveURL := strings.TrimSpace(getenv(EnvArchiveURL))
	retentionDays := strings.TrimSpace(getenv(EnvContentRetentionDays))
	var missing []string
	for _, setting := range []struct{ name, value string }{
		{EnvBigQueryProject, swap.BigQuery.Project},
		{EnvBigQueryDataset, swap.BigQuery.Dataset},
		{EnvArchiveURL, archiveURL},
		{EnvDeploymentID, swap.DeploymentID},
		{EnvContentRetentionDays, retentionDays},
	} {
		if setting.value == "" {
			missing = append(missing, setting.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("AUDIT_SINK=%s requires %s", mode, strings.Join(missing, ", "))
	}

	days, err := strconv.Atoi(retentionDays)
	if err != nil || days < 1 {
		return nil, fmt.Errorf("%s must be a whole number of days, at least 1; got %q", EnvContentRetentionDays, retentionDays)
	}
	swap.ContentRetention = time.Duration(days) * 24 * time.Hour
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
		if err != nil || wait <= 0 || wait > time.Hour {
			return nil, fmt.Errorf("%s must be a positive duration of at most an hour, like 5s; got %q", EnvRelayMaxWait, raw)
		}
		swap.RelayMaxWait = wait
	}
	return swap, nil
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
// missing, its read half on the Storage Read API — and the archive. Every
// client uses Application Default Credentials.
func Open(ctx context.Context, swap *Swap) (*Opened, error) {
	if swap == nil {
		return nil, errors.New("audit sink: no swap value to open")
	}
	opened := &Opened{}
	fail := func(err error) (*Opened, error) {
		opened.Close()
		return nil, err
	}
	if swap.Mode != business.AuditSinkBigQuery || swap.BigQuery == nil {
		return fail(fmt.Errorf("audit sink: no store of record for AUDIT_SINK=%s", swap.Mode))
	}

	bigQueryClient, err := bigquery.NewClient(ctx, swap.BigQuery.Project)
	if err != nil {
		return fail(fmt.Errorf("audit store: bigquery client: %w", err))
	}
	opened.closers = append(opened.closers, func() { _ = bigQueryClient.Close() })
	// Reads run no query job: the service holds the append grant, and append
	// plus job creation would let it run DML against the store of record.
	readClient, err := bqstorage.NewBigQueryReadClient(ctx)
	if err != nil {
		return fail(fmt.Errorf("audit store: bigquery storage read client: %w", err))
	}
	opened.closers = append(opened.closers, func() { _ = readClient.Close() })
	reader, err := bigquerystore.NewReader(bigquerystore.ReadConfig{
		Client:       readClient,
		Project:      swap.BigQuery.Project,
		Dataset:      swap.BigQuery.Dataset,
		DeploymentID: swap.DeploymentID,
	})
	if err != nil {
		return fail(err)
	}
	warehouse, err := bigquerystore.New(bigQueryClient, bigquerystore.Config{
		Dataset:                swap.BigQuery.Dataset,
		ContentDetailRetention: swap.ContentRetention,
		Reader:                 reader,
	})
	if err != nil {
		return fail(err)
	}
	ensureCtx, cancel := context.WithTimeout(ctx, time.Minute)
	err = warehouse.Ensure(ensureCtx)
	cancel()
	if err != nil {
		return fail(err)
	}
	opened.Store, opened.History = warehouse, warehouse

	if swap.Archive.Scheme != ArchiveSchemeGCS {
		return fail(fmt.Errorf("audit archive: no writer for scheme %q", swap.Archive.Scheme))
	}
	storageClient, err := storage.NewClient(ctx)
	if err != nil {
		return fail(fmt.Errorf("audit archive: storage client: %w", err))
	}
	opened.closers = append(opened.closers, func() { _ = storageClient.Close() })
	if opened.Archive, err = gcsarchive.New(storageClient, swap.Archive.Bucket); err != nil {
		return fail(err)
	}
	return opened, nil
}
