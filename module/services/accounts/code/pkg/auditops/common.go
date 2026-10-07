// Package auditops owns the audit operator commands shipped in the accounts
// executable. Commands use the deployed service's injected identity and
// configuration; they never accept database credentials in command arguments.
package auditops

import (
	"accounts/pkg/auditstore/auditsink"
	"accounts/pkg/infra"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"time"

	"github.com/codefly-dev/core/wool"
	codefly "github.com/codefly-dev/sdk-go"
)

// These commands report safe error codes. Provider logs may include URLs or
// credentials, so their logger is deliberately disabled during operator work.
type quietLog struct{}

func (quietLog) Process(*wool.Log) {}
func commandContext(ctx context.Context) (context.Context, error) {
	provider, err := codefly.Init(ctx)
	if err != nil {
		return nil, err
	}
	return provider.WithLogger(quietLog{}).Inject(ctx), nil
}

// OpenDeployedStore opens the same projected Postgres capabilities and rotating
// credentials as the accounts service. Deploy commands must initialize the
// Codefly context before resolving the store's secrets; a raw operator URL is
// deliberately handled separately by NewPostgresStoreFromURL.
func OpenDeployedStore(ctx context.Context) (*infra.PostgresStore, error) {
	ctx, err := commandContext(ctx)
	if err != nil {
		return nil, err
	}
	return infra.NewPostgresStore(ctx)
}

func openStore(ctx context.Context, testURL string) (*infra.PostgresStore, error) {
	if testURL != "" {
		return infra.NewPostgresStoreFromURL(ctx, testURL)
	}
	return infra.NewPostgresStore(ctx)
}

// safeConfiguration is what a receipt may say about where the commands pointed:
// enough to identify the warehouse, archive and deployment, and never a
// credential. A ClickHouse store is identified by its database and cluster (the
// DSN names a host and carries the password, and neither is reported).
func safeConfiguration(swap *auditsink.Swap) map[string]string {
	out := map[string]string{
		"AUDIT_SINK": string(swap.Mode), "AUDIT_DEPLOYMENT_ID": swap.DeploymentID,
		"AUDIT_ARCHIVE_URL":            swap.Archive.Scheme + "://" + swap.Archive.Bucket,
		"AUDIT_CONTENT_RETENTION_DAYS": strconv.Itoa(int(swap.ContentRetention / (24 * time.Hour))),
	}
	if swap.BigQuery != nil {
		out["AUDIT_BIGQUERY_PROJECT"] = swap.BigQuery.Project
		out["AUDIT_BIGQUERY_DATASET"] = swap.BigQuery.Dataset
	}
	if swap.ClickHouse != nil {
		out["AUDIT_CLICKHOUSE_DATABASE"] = swap.ClickHouse.Database
		out["AUDIT_CLICKHOUSE_CLUSTER"] = swap.ClickHouse.Cluster
		out["AUDIT_EVENTS_RETENTION_DAYS"] = strconv.Itoa(int(swap.ClickHouse.EventsRetention / (24 * time.Hour)))
	}
	return out
}
func writeCapabilities(out io.Writer, getenv func(string) string) int {
	cfg, err := auditsink.Load(getenv)
	if err != nil || cfg.Swap == nil {
		return 1
	}
	body := struct {
		Schema                   string            `json:"schema"`
		HistoryReport            string            `json:"history_report"`
		QualificationReport      string            `json:"qualification_report"`
		ExpectedPartitionsSHA256 bool              `json:"expected_partitions_sha256"`
		Configuration            map[string]string `json:"configuration"`
	}{"codefly/audit-tools/v1", "codefly/audit-history/v1", "codefly/audit-qualification/v1", true, safeConfiguration(cfg.Swap)}
	if json.NewEncoder(out).Encode(body) != nil {
		return 1
	}
	return 0
}
