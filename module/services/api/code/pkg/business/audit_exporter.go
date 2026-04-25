package business

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/codefly-dev/core/wool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// AuditExporter polls audit_export_configs on a 1-min cycle, finds
// configs whose last_exported_at is older than (now - cadence), and
// uploads new audit_events as JSONL to the customer's bucket.
//
// Why minio-go and not aws-sdk-go-v2: minio-go is a third the size,
// uses the same API surface against any S3-compatible store (R2,
// MinIO, GCS in S3 mode, plus AWS), and ships fewer transitive deps.
// For a starter kit the breadth-of-storage-backend matters more than
// AWS-specific feature parity (Glacier, etc) — when a customer needs
// those, they swap impls.
type AuditExporter struct {
	store    Store
	interval time.Duration
	stop     chan struct{}
}

// NewAuditExporter constructs an exporter with a 1-min polling
// cadence. Start() spawns the goroutine; Close() stops it.
func NewAuditExporter(store Store) *AuditExporter {
	return &AuditExporter{
		store:    store,
		interval: time.Minute,
		stop:     make(chan struct{}),
	}
}

// Start spawns the polling goroutine. Idempotent on repeat calls.
func (e *AuditExporter) Start() {
	go e.loop()
}

// Close signals the goroutine to stop. Safe on uninitialised values.
func (e *AuditExporter) Close() {
	if e == nil || e.stop == nil {
		return
	}
	select {
	case <-e.stop:
		// already closed
	default:
		close(e.stop)
	}
}

func (e *AuditExporter) loop() {
	t := time.NewTicker(e.interval)
	defer t.Stop()
	// Run an immediate tick on startup so a freshly-deployed api
	// doesn't sit idle for a full minute on a customer who just
	// configured exports.
	e.tick(context.Background())
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			e.tick(context.Background())
		}
	}
}

func (e *AuditExporter) tick(ctx context.Context) {
	w := wool.Get(ctx).In("AuditExporter.tick")
	configs, err := e.store.ListDueAuditExportConfigs(ctx, time.Now())
	if err != nil {
		w.Debug("ListDueAuditExportConfigs failed", wool.ErrField(err))
		return
	}
	for _, cfg := range configs {
		// One config at a time, sequentially. Concurrent exports per
		// config aren't useful (each must read since last_exported_at
		// from the same row) and inter-org parallelism is unnecessary
		// at the volumes a single api instance handles.
		e.exportOne(ctx, cfg)
	}
}

// exportOne runs one export cycle for one config: pulls events since
// last_exported_at, uploads as JSONL, advances last_exported_at on
// success or records last_error on failure. Either way it returns —
// the next tick re-evaluates the same config.
func (e *AuditExporter) exportOne(ctx context.Context, cfg *AuditExportConfig) {
	w := wool.Get(ctx).In("AuditExporter.exportOne", wool.Field("org", cfg.OrgID))

	// Window: from last_exported_at (or epoch) to now. Capped to
	// 50k events per run so a backlog doesn't OOM the api on first
	// export of a high-volume org.
	from := time.Time{}
	if cfg.LastExportedAt != nil {
		from = *cfg.LastExportedAt
	}
	to := time.Now()

	events, _, _, err := e.store.QueryAuditLog(
		ctx, cfg.OrgID, "", "", "", "", &from, &to, 50_000, "")
	if err != nil {
		_ = e.store.RecordAuditExportError(ctx, cfg.OrgID, err.Error())
		w.Debug("query failed", wool.ErrField(err))
		return
	}
	if len(events) == 0 {
		// Nothing to export — still advance the cursor so we don't
		// requery the same empty window next tick.
		_ = e.store.MarkAuditExportSucceeded(ctx, cfg.OrgID, to)
		return
	}

	body, err := encodeJSONL(events)
	if err != nil {
		_ = e.store.RecordAuditExportError(ctx, cfg.OrgID, err.Error())
		return
	}

	if err := e.upload(ctx, cfg, to, body); err != nil {
		_ = e.store.RecordAuditExportError(ctx, cfg.OrgID, err.Error())
		w.Debug("upload failed", wool.ErrField(err))
		return
	}

	_ = e.store.MarkAuditExportSucceeded(ctx, cfg.OrgID, to)
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
		if err := enc.Encode(ev); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// upload writes body to the customer's bucket at:
//   <prefix>/<yyyy-mm-dd>/<unix-ms>.jsonl
//
// `prefix` may be empty. The unix-ms suffix dedupes when an exporter
// runs multiple times in the same second (e.g. two api instances
// fighting over the same row — which the lock should prevent but
// the suffix is belt-and-suspenders).
func (e *AuditExporter) upload(ctx context.Context, cfg *AuditExportConfig, ts time.Time, body []byte) error {
	endpoint := cfg.Endpoint
	useSSL := true
	if endpoint == "" {
		// Real AWS S3 — minio-go expects host without scheme.
		endpoint = "s3." + cfg.Region + ".amazonaws.com"
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: useSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return fmt.Errorf("s3 client: %w", err)
	}

	day := ts.UTC().Format("2006-01-02")
	objectKey := joinPath(cfg.Prefix, day, fmt.Sprintf("%d.jsonl", ts.UnixMilli()))

	_, err = client.PutObject(ctx, cfg.Bucket, objectKey,
		bytes.NewReader(body), int64(len(body)),
		minio.PutObjectOptions{ContentType: "application/x-ndjson"})
	if err != nil {
		return fmt.Errorf("put: %w", err)
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
