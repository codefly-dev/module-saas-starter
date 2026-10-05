// Command audit-history-copy is the one-time history copy of a deployment
// switching from AUDIT_SINK=postgres to a swap value (ADR 0009, item 7). It
// copies the audit_events rows the deployment already holds into the store of
// record and the locked archive — each event classified, serialized and hashed
// exactly as the relay does a live one — and verifies the copy by reading it
// back: every row present, every copy identical, per organization. Only with
// -confirm-drop, and only after this run verified every copied partition, it
// drops the copied monthly partitions, the way retention drops them; no row is
// ever deleted.
//
// Usage:
//
//	audit-history-copy [-through YYYY-MM] [-verify-only] [-confirm-drop -expected-partitions-sha256 HEX] [-json]
//
// It reads the same AUDIT_* settings as the accounts service and runs only
// under a swap value: run it with the service's own environment, after the
// service has switched, so nothing writes audit_events any more. The
// connection principal must be a member of app_control_plane, the authority
// retention runs under.
//
// It is resumable: a run reads back what the store holds before writing, so a
// run after an interrupted one copies only what is missing. -verify-only
// copies nothing, for a re-check once the store has caught up.
//
// Exit status: 0 done; 1 failed; 2 usage; 3 verification failed, nothing
// dropped.
package auditops

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"sort"
	"time"

	"accounts/pkg/auditstore/auditsink"
	"accounts/pkg/business"
)

// Writes to the process's stdout/stderr have no meaningful recovery.
func line(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }

// options is a parsed command line.
type options struct {
	databaseURL  string
	through      *time.Time
	batchSize    int
	copyOptions  business.AuditHistoryCopyOptions
	json         bool
	capabilities bool
	throughMonth string
}

// parse reads the command line and the sink settings, refusing everything a
// run cannot start from.
func parse(args []string, getenv func(string) string, stderr io.Writer) (options, *auditsink.Swap, int) {
	fs := flag.NewFlagSet("audit-history-copy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	databaseURL := getenv("DATABASE_URL")
	machine := fs.Bool("json", false, "emit a sanitized machine receipt")
	capabilities := fs.Bool("capabilities-json", false, "report supported command schemas and destination without connecting")
	expected := fs.String("expected-partitions-sha256", "", "digest from prior verification, required for drop")
	through := fs.String("through", "", "copy only the partitions up to and including this month, YYYY-MM (default every partition)")
	verifyOnly := fs.Bool("verify-only", false, "copy nothing; verify what the store already holds")
	confirmDrop := fs.Bool("confirm-drop", false, "after a verification pass, drop the copied partitions")
	batchSize := fs.Int("batch-size", business.DefaultAuditRelayBatchSize, "events per copied batch")
	if err := fs.Parse(args); err != nil {
		return options{}, nil, 2
	}
	if fs.NArg() > 0 {
		line(stderr, "audit-history-copy: unexpected arguments:", fs.Args())
		return options{}, nil, 2
	}
	opts := options{
		databaseURL: databaseURL,
		json:        *machine, capabilities: *capabilities, throughMonth: *through,
		batchSize:   *batchSize,
		copyOptions: business.AuditHistoryCopyOptions{VerifyOnly: *verifyOnly, ConfirmDrop: *confirmDrop, ExpectedPartitionsSHA256: *expected},
	}
	if *through != "" {
		monthStart, err := time.Parse("2006-01", *through)
		if err != nil {
			line(stderr, "audit-history-copy: -through must be a month, YYYY-MM; got", *through)
			return options{}, nil, 2
		}
		end := monthStart.AddDate(0, 1, 0)
		opts.through = &end
	}
	if *confirmDrop && (*through == "" || !*verifyOnly || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(*expected)) {
		line(stderr, "audit-history-copy: drop requires -through YYYY-MM, -verify-only and -expected-partitions-sha256")
		return options{}, nil, 2
	}
	sink, err := auditsink.Load(getenv)
	if err != nil {
		line(stderr, "audit-history-copy: invalid audit destination configuration")
		return options{}, nil, 1
	}
	if sink.Swap == nil {
		line(stderr, fmt.Sprintf("audit-history-copy: AUDIT_SINK is %s; the history copy runs only under a swap value, "+
			"after the service has switched, so nothing writes audit_events any more", sink.Mode))
		return options{}, nil, 1
	}
	return opts, sink.Swap, 0
}

func RunHistory(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	opts, swap, code := parse(args, getenv, stderr)
	if swap == nil {
		return code
	}
	if opts.capabilities {
		return writeCapabilities(stdout, getenv)
	}
	ctx, err := commandContext(context.Background())
	if err != nil {
		return historyFailure(stdout, stderr, opts, swap, "runtime_configuration_failed")
	}
	store, err := openStore(ctx, opts.databaseURL)
	if err != nil {
		return historyFailure(stdout, stderr, opts, swap, "operation_failed")
	}
	defer store.Close()
	opened, err := auditsink.Open(ctx, swap)
	if err != nil {
		return historyFailure(stdout, stderr, opts, swap, "operation_failed")
	}
	defer opened.Close()

	copier, err := business.NewAuditHistoryCopy(business.AuditHistoryCopyConfig{
		Source:           store,
		Store:            opened.Store,
		Archive:          opened.Archive,
		ReadBack:         opened.History,
		Types:            store,
		DeploymentID:     swap.DeploymentID,
		ContentRetention: swap.ContentRetention,
		BatchSize:        opts.batchSize,
		Through:          opts.through,
		Progress: func(message string) {
			if !opts.json {
				line(stdout, message)
			}
		},
	})
	if err != nil {
		return historyFailure(stdout, stderr, opts, swap, "invalid_options")
	}
	report, err := copier.Run(ctx, opts.copyOptions)
	if opts.json {
		return writeHistory(stdout, opts, swap, report, err)
	}
	return summarize(stdout, stderr, report, err)
}

// summarize prints what the run found and maps it to the exit status. Pure over
// its inputs so the mapping is testable without a database or a warehouse.
func summarize(stdout, stderr io.Writer, report business.AuditHistoryReport, err error) int {
	for _, partition := range report.Partitions {
		orgs := make([]string, 0, len(partition.Orgs))
		for org := range partition.Orgs {
			orgs = append(orgs, org)
		}
		sort.Strings(orgs)
		for _, org := range orgs {
			count := partition.Orgs[org]
			name := org
			if name == "" {
				name = "(platform)"
			}
			line(stdout, fmt.Sprintf("  %s %s: %d in Postgres, %d verified", partition.Partition.Name, name, count.Postgres, count.Verified))
		}
		for _, problem := range partition.Problems {
			line(stderr, fmt.Sprintf("  %s: %s", partition.Partition.Name, problem))
		}
		if hidden := partition.Failures - len(partition.Problems); hidden > 0 {
			line(stderr, fmt.Sprintf("  %s: and %d more", partition.Partition.Name, hidden))
		}
	}
	switch {
	case errors.Is(err, business.ErrAuditHistoryUnverified):
		line(stderr, "audit-history-copy:", err)
		return 3
	case err != nil:
		line(stderr, "audit-history-copy: operation_failed")
		return 1
	case report.Dropped > 0:
		line(stdout, fmt.Sprintf("verified; dropped %d partitions ending at or before %s", report.Dropped, report.Cutoff.UTC().Format(time.RFC3339)))
	default:
		line(stdout, "verified")
	}
	return 0
}

// HistoryReceipt is the public machine evidence. It excludes payloads, secrets
// and provider errors; the digest still binds every source event and hash.
type HistoryReceipt struct {
	Schema           string             `json:"schema"`
	DeploymentID     string             `json:"deployment_id"`
	Sink             string             `json:"sink"`
	Configuration    map[string]string  `json:"configuration"`
	Through          string             `json:"through"`
	ObservedAt       time.Time          `json:"observed_at"`
	Verified         bool               `json:"verified"`
	PartitionsSHA256 string             `json:"partitions_sha256"`
	Partitions       []HistoryPartition `json:"partitions"`
	Dropped          []string           `json:"dropped"`
	Cutoff           time.Time          `json:"cutoff"`
	ErrorCode        string             `json:"error_code"`
}
type HistoryPartition struct {
	Name         string       `json:"name"`
	From         time.Time    `json:"from"`
	To           time.Time    `json:"to"`
	Orgs         []HistoryOrg `json:"orgs"`
	Failures     int          `json:"failures"`
	EventsSHA256 string       `json:"events_sha256"`
}
type HistoryOrg struct {
	OrgID    string `json:"org_id"`
	Postgres int64  `json:"postgres"`
	Verified int64  `json:"verified"`
}

func writeHistory(out io.Writer, opts options, swap *auditsink.Swap, report business.AuditHistoryReport, err error) int {
	receipt := HistoryReceipt{Schema: "codefly/audit-history/v1", DeploymentID: swap.DeploymentID, Sink: string(swap.Mode), Configuration: safeConfiguration(swap), Through: opts.throughMonth, ObservedAt: time.Now().UTC(), Verified: report.Verified, PartitionsSHA256: report.PartitionsSHA256, Partitions: []HistoryPartition{}, Dropped: report.DroppedNames, Cutoff: report.Cutoff}
	if receipt.Dropped == nil {
		receipt.Dropped = []string{}
	}
	code := 0
	if err != nil {
		code = 1
		receipt.ErrorCode = "operation_failed"
	}
	if errors.Is(err, business.ErrAuditHistoryUnverified) {
		code = 3
		receipt.ErrorCode = "verification_failed"
	}
	if report.DropRefused != "" {
		receipt.ErrorCode = "drop_refused"
	}
	for _, p := range report.Partitions {
		part := HistoryPartition{Name: p.Partition.Name, From: p.Partition.From, To: p.Partition.To, Failures: p.Failures, EventsSHA256: p.EventsSHA256, Orgs: []HistoryOrg{}}
		ids := make([]string, 0, len(p.Orgs))
		for id := range p.Orgs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			c := p.Orgs[id]
			part.Orgs = append(part.Orgs, HistoryOrg{id, c.Postgres, c.Verified})
		}
		receipt.Partitions = append(receipt.Partitions, part)
	}
	if json.NewEncoder(out).Encode(receipt) != nil {
		return 1
	}
	return code
}
func historyFailure(out, stderr io.Writer, opts options, swap *auditsink.Swap, code string) int {
	if opts.json {
		return writeHistory(out, opts, swap, business.AuditHistoryReport{}, errors.New(code))
	}
	line(stderr, "audit-history-copy:", code)
	return 1
}
