package business

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// AuditExporter executes one already-durable audit export job. Scheduling,
// claims, retries, dead letters, and shutdown belong to the generic job
// platform; this type owns only product reads, object upload, and projection.
//
// Why minio-go and not aws-sdk-go-v2: minio-go is a third the size,
// uses the same API surface against any S3-compatible store (R2,
// MinIO, GCS in S3 mode, plus AWS), and ships fewer transitive deps.
// For a starter kit the breadth-of-storage-backend matters more than
// AWS-specific feature parity (Glacier, etc) — when a customer needs
// those, they swap impls.
type AuditExporter struct {
	store     Store
	interval  time.Duration
	stop      chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

// NewAuditExporter constructs the product-specific one-shot processor.
func NewAuditExporter(store Store) *AuditExporter {
	return &AuditExporter{
		store:    store,
		interval: time.Minute,
		stop:     make(chan struct{}),
	}
}

// Start schedules configured exports until the durable export scheduler owns
// this trigger. Export itself stays one-shot so the trigger can be replaced
// without changing product processing or storage behavior.
func (e *AuditExporter) Start() {
	if e == nil {
		return
	}
	e.startOnce.Do(func() { go e.loop() })
}

// Close stops new scheduled exports. An export already in progress is allowed
// to finish before the loop exits.
func (e *AuditExporter) Close() {
	if e == nil {
		return
	}
	e.closeOnce.Do(func() { close(e.stop) })
}

func (e *AuditExporter) loop() {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	e.tick(context.Background())
	for {
		select {
		case <-e.stop:
			return
		case <-ticker.C:
			e.tick(context.Background())
		}
	}
}

func (e *AuditExporter) tick(ctx context.Context) {
	w := wool.Get(ctx).In("AuditExporter.tick")
	var configs []*AuditExportConfig
	if err := e.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		configs, err = e.store.ListDueAuditExportConfigs(ctx, time.Now().UTC())
		return err
	}); err != nil {
		w.Debug("list due export configs failed", wool.ErrField(err))
		return
	}
	for _, config := range configs {
		if err := e.Export(ctx, config, uuid.NewString()); err != nil {
			w.Debug("scheduled export failed",
				wool.Field("org", config.OrgID),
				wool.ErrField(err),
			)
		}
	}
}

// Export runs one export cycle for one config. objectID is the durable job UUID,
// making object replacement idempotent across automatic retries.
func (e *AuditExporter) Export(ctx context.Context, cfg *AuditExportConfig, objectID string) error {
	w := wool.Get(ctx).In("AuditExporter.Export", wool.Field("org", cfg.OrgID))

	// Window: from last_exported_at (or epoch) to now. Capped to
	// 50k events per run so a backlog doesn't OOM the api on first
	// export of a high-volume org.
	from := time.Time{}
	if cfg.LastExportedAt != nil {
		// PostgreSQL timestamps have microsecond precision. The previous cursor
		// is inclusive in the shared query API, so advance by one database tick
		// to avoid duplicating the boundary event in the next object.
		from = cfg.LastExportedAt.Add(time.Microsecond)
	}
	to := time.Now().UTC()

	// All per-org reads + writes for this cycle run inside a single
	// WithOrgTx so the RLS policy on audit_export_configs scopes
	// MarkSucceeded / RecordError to this org. QueryAuditLog goes
	// through too even though audit_events doesn't yet have RLS — when
	// it does (Phase 2), this code path will Just Work.
	var body bytes.Buffer
	pageToken := ""
	eventCount := 0
	if err := e.store.WithOrgTx(ctx, cfg.OrgID, func(ctx context.Context) error {
		for {
			events, nextToken, _, err := e.store.QueryAuditLog(
				ctx, AuditQuery{OrgID: cfg.OrgID, From: &from, To: &to, PageSize: 5_000, PageToken: pageToken})
			if err != nil {
				return err
			}
			chunk, err := encodeJSONL(events)
			if err != nil {
				return err
			}
			if _, err := body.Write(chunk); err != nil {
				return err
			}
			eventCount += len(events)
			if nextToken == "" {
				return nil
			}
			pageToken = nextToken
		}
	}); err != nil {
		_ = e.recordError(ctx, cfg.OrgID, "query_failed")
		w.Debug("query failed", wool.ErrField(err))
		return err
	}
	if eventCount == 0 {
		// Nothing to export — still advance the cursor so we don't
		// requery the same empty window next tick.
		if err := e.markSucceeded(ctx, cfg.OrgID, to); err != nil {
			return err
		}
		return nil
	}

	if err := e.upload(ctx, cfg, to, objectID, body.Bytes()); err != nil {
		_ = e.recordError(ctx, cfg.OrgID, "storage_failed")
		w.Debug("upload failed", wool.ErrField(err))
		return err
	}

	if err := e.markSucceeded(ctx, cfg.OrgID, to); err != nil {
		return err
	}
	return nil
}

// markSucceeded / recordError — small wrappers so the per-tenant
// state mutations land inside WithOrgTx, satisfying the RLS policy
// on audit_export_configs.
func (e *AuditExporter) markSucceeded(ctx context.Context, orgID string, exportedAt time.Time) error {
	return e.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return e.store.MarkAuditExportSucceeded(ctx, orgID, exportedAt)
	})
}

func (e *AuditExporter) recordError(ctx context.Context, orgID, message string) error {
	return e.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return e.store.RecordAuditExportError(ctx, orgID, message)
	})
}

// encodeJSONL serializes audit entries one-per-line (the standard
// "JSON Lines" format used by SIEMs and Athena queries). Each line
// is independently parseable, so partial reads still surface valid
// rows.
func encodeJSONL(events []AuditEntry) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, ev := range events {
		// Encode each event individually. Encoder writes a newline
		// after each record by default — matches JSONL conventions.
		// The export projection is PII-redacted (auditEntryToExport) so the
		// customer's bucket never receives classified payload fields.
		if err := enc.Encode(auditEntryToExport(ev)); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// upload writes body to the customer's bucket at:
//
//	<prefix>/<yyyy-mm-dd>/<unix-ms>.jsonl
//
// `prefix` may be empty. The unix-ms suffix dedupes when an exporter
// runs multiple times in the same second (e.g. two api instances
// fighting over the same row — which the lock should prevent but
// the suffix is belt-and-suspenders).
//
// Bucket auto-create: if the bucket doesn't exist on first upload,
// create it. Most production deployments pre-create the bucket with
// IAM/lifecycle/encryption policies; the auto-create branch is for
// dev (local MinIO) where the operator just typed in a name. Errors
// other than "already exists" are surfaced.
func (e *AuditExporter) upload(ctx context.Context, cfg *AuditExportConfig, ts time.Time, objectID string, body []byte) error {
	endpoint, useSSL := resolveEndpoint(cfg.Endpoint, cfg.Region)

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: useSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return fmt.Errorf("s3 client: %w", err)
	}

	if err := e.ensureBucket(ctx, client, cfg.Bucket, cfg.Region); err != nil {
		return fmt.Errorf("ensure bucket: %w", err)
	}

	day := ts.UTC().Format("2006-01-02")
	objectKey := joinPath(cfg.Prefix, day, objectID+".jsonl")

	_, err = client.PutObject(ctx, cfg.Bucket, objectKey,
		bytes.NewReader(body), int64(len(body)),
		minio.PutObjectOptions{ContentType: "application/x-ndjson"})
	if err != nil {
		return fmt.Errorf("put: %w", err)
	}
	return nil
}

// VerifyAuditExportConnection probes the configured S3 endpoint with
// the supplied creds, returning nil if the bucket either exists or
// can be created. Used by SaveAuditExportConfig as a pre-flight
// check so a typo in the access key or a wrong region surfaces in
// the operator's Save toast — not silently in last_error a minute
// later.
//
// Side-effect-free against existing buckets: only calls BucketExists.
// We deliberately do NOT call MakeBucket here — the exporter does
// that on its first real upload. Validating "I can authenticate +
// the bucket exists OR I have permission to make it" is the contract
// we want to assert at config-save time without leaving an empty
// bucket behind if the operator changes their mind before exports
// actually run.
func VerifyAuditExportConnection(ctx context.Context, cfg *AuditExportConfig) error {
	endpoint, useSSL := resolveEndpoint(cfg.Endpoint, cfg.Region)
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: useSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return fmt.Errorf("s3 client: %w", err)
	}
	// BucketExists is HEAD on the bucket — minimal cost, surfaces
	// 401/403 on bad creds, 404 on missing bucket (which is fine —
	// it just means the exporter will create it on first upload),
	// and network errors on a typo'd endpoint.
	if _, err := client.BucketExists(ctx, cfg.Bucket); err != nil {
		errResp := minio.ToErrorResponse(err)
		// 404 / NoSuchBucket = bucket missing. That's not a failure
		// for the verify step — ensureBucket will create it on the
		// first export. The operator just needs to know auth + endpoint
		// are valid.
		if errResp.Code == "NoSuchBucket" || errResp.StatusCode == 404 {
			return nil
		}
		return fmt.Errorf("bucket probe: %w", err)
	}
	return nil
}

// resolveEndpoint converts a user-entered endpoint string into the
// (host, useSSL) pair minio-go needs. Accepts:
//
//	""                    → AWS S3 in `region`, TLS on
//	"host:port"           → TLS on  (production-shaped)
//	"https://host:port"   → TLS on
//	"http://host:port"    → TLS OFF (local MinIO / non-prod)
//
// Letting the operator opt into HTTP via the http:// prefix avoids a
// separate use_tls field on the config (and the migration / proto
// churn that comes with it). The scheme is the cleanest place to put
// this signal since "http://" already MEANS no TLS to anyone reading
// the form.
func resolveEndpoint(endpoint, region string) (string, bool) {
	if endpoint == "" {
		return "s3." + region + ".amazonaws.com", true
	}
	switch {
	case len(endpoint) > 8 && endpoint[:8] == "https://":
		return endpoint[8:], true
	case len(endpoint) > 7 && endpoint[:7] == "http://":
		return endpoint[7:], false
	default:
		return endpoint, true
	}
}

// ensureBucket creates the bucket if it doesn't exist. minio-go's
// MakeBucket returns an error wrapped as ErrorResponse with
// Code=BucketAlreadyOwnedByYou (or BucketAlreadyExists) on a re-run;
// those are not failures. Anything else (NoSuchEndpoint, AccessDenied,
// network) propagates so the exporter surfaces it as last_error.
func (e *AuditExporter) ensureBucket(ctx context.Context, client *minio.Client, bucket, region string) error {
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: region}); err != nil {
		// Race: another exporter created it between our check and our
		// create. Treat as success.
		errResp := minio.ToErrorResponse(err)
		if errResp.Code == "BucketAlreadyOwnedByYou" || errResp.Code == "BucketAlreadyExists" {
			return nil
		}
		return err
	}
	return nil
}

// joinPath concatenates path segments with a single slash, trimming
// duplicate / leading slashes so consumers see clean keys regardless
// of whether the operator typed `prefix=audit/` or `prefix=audit`.
func joinPath(parts ...string) string {
	out := ""
	for _, p := range parts {
		for len(p) > 0 && p[0] == '/' {
			p = p[1:]
		}
		for len(p) > 0 && p[len(p)-1] == '/' {
			p = p[:len(p)-1]
		}
		if p == "" {
			continue
		}
		if out == "" {
			out = p
		} else {
			out = out + "/" + p
		}
	}
	return out
}
