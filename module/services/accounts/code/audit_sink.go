package main

import (
	"accounts/pkg/auditstore/bigquerystore"
	"accounts/pkg/auditstore/gcsarchive"
	"accounts/pkg/business"
	"accounts/pkg/infra"
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/storage"
)

// The audit sink settings. AUDIT_SINK selects the mode (business.AuditSinkMode);
// each mode's settings are required only under it, and a missing one fails
// startup rather than degrading the audit trail.
const (
	envAuditSink = "AUDIT_SINK"

	// AUDIT_SINK=both (ADR 0006).
	envAuditExternalURL   = "AUDIT_EXTERNAL_URL"
	envAuditExternalToken = "AUDIT_EXTERNAL_TOKEN"

	// AUDIT_SINK=bigquery (ADR 0009). Credentials are Application Default
	// Credentials only — on Kubernetes, the pod's workload identity — so there
	// is deliberately no key-file or token setting.
	envAuditBigQueryProject      = "AUDIT_BIGQUERY_PROJECT"
	envAuditBigQueryDataset      = "AUDIT_BIGQUERY_DATASET"
	envAuditArchiveURL           = "AUDIT_ARCHIVE_URL"
	envAuditDeploymentID         = "AUDIT_DEPLOYMENT_ID"
	envAuditContentRetentionDays = "AUDIT_CONTENT_RETENTION_DAYS"
	// Optional relay tuning.
	envAuditRelayBatchSize = "AUDIT_RELAY_BATCH_SIZE"
	envAuditRelayMaxWait   = "AUDIT_RELAY_MAX_WAIT"
)

// auditSinkConfig is the resolved audit sink: the mode and, for the modes that
// write outside Postgres, where.
type auditSinkConfig struct {
	mode business.AuditSinkMode
	// external is the tee's destination under AUDIT_SINK=both.
	external business.ExternalAuditSink
	// bigQuery is the store of record and archive under AUDIT_SINK=bigquery.
	bigQuery *bigQueryAuditSwap
}

// configuredAuditSink reads AUDIT_SINK and the settings its mode requires.
// "postgres" (the default) keeps the durable emitter alone. "both" adds an
// asynchronous tee to an HTTP endpoint while audit_events stays the store of
// record. "bigquery" swaps the store of record to BigQuery, with Postgres
// keeping only the transactional queue. "external" is refused: no destination
// outside Postgres can join the transaction a change commits in.
func configuredAuditSink() (auditSinkConfig, error) {
	mode, err := business.ParseAuditSinkMode(os.Getenv(envAuditSink))
	if err != nil {
		return auditSinkConfig{}, err
	}
	config := auditSinkConfig{mode: mode}
	switch mode {
	case business.AuditSinkBoth:
		config.external, err = configuredExternalAuditSink()
	case business.AuditSinkBigQuery:
		config.bigQuery, err = configuredBigQueryAuditSwap()
	}
	if err != nil {
		return auditSinkConfig{}, err
	}
	return config, nil
}

func configuredExternalAuditSink() (business.ExternalAuditSink, error) {
	endpoint := strings.TrimSpace(os.Getenv(envAuditExternalURL))
	if endpoint == "" {
		return nil, fmt.Errorf("AUDIT_SINK=both requires %s for the external audit destination", envAuditExternalURL)
	}
	return business.NewHTTPAuditSink(business.HTTPAuditSinkConfig{
		Endpoint: endpoint,
		Token:    strings.TrimSpace(os.Getenv(envAuditExternalToken)),
	})
}

// bigQueryAuditSwap is the validated AUDIT_SINK=bigquery configuration.
type bigQueryAuditSwap struct {
	project                string
	dataset                string
	archive                auditArchiveLocation
	deploymentID           string
	contentDetailRetention time.Duration
	batchSize              int
	maxWait                time.Duration
}

// auditArchiveLocation is a parsed AUDIT_ARCHIVE_URL. The scheme picks the
// archive writer; gs (Google Cloud Storage) is the one built.
type auditArchiveLocation struct {
	scheme string
	bucket string
}

// auditArchiveSchemeGCS is the AUDIT_ARCHIVE_URL scheme of a GCS bucket.
const auditArchiveSchemeGCS = "gs"

// gcsBucketNamePattern is GCS's bucket naming rule, loosely: lowercase
// letters, digits, '-', '_' and '.', starting and ending alphanumeric.
var gcsBucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$`)

// parseAuditArchiveURL reads AUDIT_ARCHIVE_URL: gs://<bucket>, nothing more.
// Another scheme names an archive no writer exists for yet, which is refused
// at startup rather than left to fail at the first delivery.
func parseAuditArchiveURL(raw string) (auditArchiveLocation, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return auditArchiveLocation{}, fmt.Errorf("%s must be a URL like gs://<bucket>; got %q", envAuditArchiveURL, raw)
	}
	if parsed.Scheme != auditArchiveSchemeGCS {
		return auditArchiveLocation{}, fmt.Errorf("%s scheme %q has no archive writer; the supported scheme is %s:// (got %q)",
			envAuditArchiveURL, parsed.Scheme, auditArchiveSchemeGCS, raw)
	}
	if parsed.User != nil || parsed.Port() != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return auditArchiveLocation{}, fmt.Errorf("%s names a bucket and nothing else, like gs://<bucket>; got %q", envAuditArchiveURL, raw)
	}
	if !gcsBucketNamePattern.MatchString(parsed.Host) {
		return auditArchiveLocation{}, fmt.Errorf("%s bucket %q is not a valid bucket name", envAuditArchiveURL, parsed.Host)
	}
	return auditArchiveLocation{scheme: parsed.Scheme, bucket: parsed.Host}, nil
}

// auditDeploymentIDPattern keeps the deployment id usable as an archive path
// segment and a warehouse value without escaping.
var auditDeploymentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// configuredBigQueryAuditSwap reads and validates every AUDIT_SINK=bigquery
// setting, reporting all the missing ones at once.
func configuredBigQueryAuditSwap() (*bigQueryAuditSwap, error) {
	swap := &bigQueryAuditSwap{
		project:      strings.TrimSpace(os.Getenv(envAuditBigQueryProject)),
		dataset:      strings.TrimSpace(os.Getenv(envAuditBigQueryDataset)),
		deploymentID: strings.TrimSpace(os.Getenv(envAuditDeploymentID)),
		batchSize:    business.DefaultAuditRelayBatchSize,
		maxWait:      business.DefaultAuditRelayMaxWait,
	}
	archiveURL := strings.TrimSpace(os.Getenv(envAuditArchiveURL))
	retentionDays := strings.TrimSpace(os.Getenv(envAuditContentRetentionDays))
	var missing []string
	for _, setting := range []struct{ name, value string }{
		{envAuditBigQueryProject, swap.project},
		{envAuditBigQueryDataset, swap.dataset},
		{envAuditArchiveURL, archiveURL},
		{envAuditDeploymentID, swap.deploymentID},
		{envAuditContentRetentionDays, retentionDays},
	} {
		if setting.value == "" {
			missing = append(missing, setting.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("AUDIT_SINK=bigquery requires %s", strings.Join(missing, ", "))
	}

	days, err := strconv.Atoi(retentionDays)
	if err != nil || days < 1 {
		return nil, fmt.Errorf("%s must be a whole number of days, at least 1; got %q", envAuditContentRetentionDays, retentionDays)
	}
	swap.contentDetailRetention = time.Duration(days) * 24 * time.Hour
	if !auditDeploymentIDPattern.MatchString(swap.deploymentID) {
		return nil, fmt.Errorf("%s must be letters, digits, '.', '_' or '-' (at most 128, starting with a letter or digit); got %q",
			envAuditDeploymentID, swap.deploymentID)
	}
	if swap.archive, err = parseAuditArchiveURL(archiveURL); err != nil {
		return nil, err
	}
	if raw := strings.TrimSpace(os.Getenv(envAuditRelayBatchSize)); raw != "" {
		size, err := strconv.Atoi(raw)
		if err != nil || size < 1 || size > business.MaxAuditRelayBatchSize {
			return nil, fmt.Errorf("%s must be between 1 and %d; got %q", envAuditRelayBatchSize, business.MaxAuditRelayBatchSize, raw)
		}
		swap.batchSize = size
	}
	if raw := strings.TrimSpace(os.Getenv(envAuditRelayMaxWait)); raw != "" {
		wait, err := time.ParseDuration(raw)
		if err != nil || wait <= 0 || wait > time.Hour {
			return nil, fmt.Errorf("%s must be a positive duration of at most an hour, like 5s; got %q", envAuditRelayMaxWait, raw)
		}
		swap.maxWait = wait
	}
	return swap, nil
}

// newBigQueryAuditRelay builds the relay of the bigquery swap value: the
// BigQuery store of record (its tables created if missing), the GCS archive,
// and the queue on the relay's own pool. Both clients use Application Default
// Credentials. The returned close releases them all.
func newBigQueryAuditRelay(ctx context.Context, types business.DeclaredAuditEventTypeReader, swap *bigQueryAuditSwap) (*business.AuditRelay, func(), error) {
	var closers []func()
	closeAll := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	fail := func(err error) (*business.AuditRelay, func(), error) {
		closeAll()
		return nil, nil, err
	}

	bigQueryClient, err := bigquery.NewClient(ctx, swap.project)
	if err != nil {
		return fail(fmt.Errorf("audit store: bigquery client: %w", err))
	}
	closers = append(closers, func() { _ = bigQueryClient.Close() })
	warehouse, err := bigquerystore.New(bigQueryClient, bigquerystore.Config{
		Dataset:                swap.dataset,
		ContentDetailRetention: swap.contentDetailRetention,
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

	if swap.archive.scheme != auditArchiveSchemeGCS {
		return fail(fmt.Errorf("audit archive: no writer for scheme %q", swap.archive.scheme))
	}
	storageClient, err := storage.NewClient(ctx)
	if err != nil {
		return fail(fmt.Errorf("audit archive: storage client: %w", err))
	}
	closers = append(closers, func() { _ = storageClient.Close() })
	archive, err := gcsarchive.New(storageClient, swap.archive.bucket)
	if err != nil {
		return fail(err)
	}

	pool, err := infra.NewAuditRelayPool(ctx)
	if err != nil {
		return fail(fmt.Errorf("audit relay: database pool: %w", err))
	}
	closers = append(closers, pool.Close)
	queue, err := infra.NewPostgresAuditQueue(pool)
	if err != nil {
		return fail(err)
	}

	relay, err := business.NewAuditRelay(business.AuditRelayConfig{
		Queue:        queue,
		Store:        warehouse,
		Archive:      archive,
		Types:        types,
		DeploymentID: swap.deploymentID,
		BatchSize:    swap.batchSize,
		MaxWait:      swap.maxWait,
	})
	if err != nil {
		return fail(err)
	}
	return relay, closeAll, nil
}
