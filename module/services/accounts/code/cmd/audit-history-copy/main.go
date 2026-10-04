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
//	audit-history-copy [-database-url "$DATABASE_URL"] [-through YYYY-MM] [-verify-only] [-confirm-drop] [-batch-size N]
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
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"accounts/pkg/auditstore/auditsink"
	"accounts/pkg/business"
	"accounts/pkg/infra"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// Writes to the process's stdout/stderr have no meaningful recovery.
func line(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }

// options is a parsed command line.
type options struct {
	databaseURL string
	through     *time.Time
	batchSize   int
	copyOptions business.AuditHistoryCopyOptions
}

// parse reads the command line and the sink settings, refusing everything a
// run cannot start from.
func parse(args []string, getenv func(string) string, stderr io.Writer) (options, *auditsink.Swap, int) {
	fs := flag.NewFlagSet("audit-history-copy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	databaseURL := fs.String("database-url", getenv("DATABASE_URL"), "Postgres connection URL (defaults to $DATABASE_URL)")
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
		databaseURL: *databaseURL,
		batchSize:   *batchSize,
		copyOptions: business.AuditHistoryCopyOptions{VerifyOnly: *verifyOnly, ConfirmDrop: *confirmDrop},
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
	if opts.databaseURL == "" {
		line(stderr, "audit-history-copy: -database-url (or $DATABASE_URL) is required")
		return options{}, nil, 2
	}
	sink, err := auditsink.Load(getenv)
	if err != nil {
		line(stderr, "audit-history-copy:", err)
		return options{}, nil, 1
	}
	if sink.Swap == nil {
		line(stderr, fmt.Sprintf("audit-history-copy: AUDIT_SINK is %s; the history copy runs only under a swap value, "+
			"after the service has switched, so nothing writes audit_events any more", sink.Mode))
		return options{}, nil, 1
	}
	return opts, sink.Swap, 0
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	opts, swap, code := parse(args, getenv, stderr)
	if swap == nil {
		return code
	}
	ctx := context.Background()
	store, err := infra.NewPostgresStoreFromURL(ctx, opts.databaseURL)
	if err != nil {
		line(stderr, "audit-history-copy:", err)
		return 1
	}
	defer store.Close()
	opened, err := auditsink.Open(ctx, swap)
	if err != nil {
		line(stderr, "audit-history-copy:", err)
		return 1
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
		Progress:         func(message string) { line(stdout, message) },
	})
	if err != nil {
		line(stderr, "audit-history-copy:", err)
		return 2
	}
	report, err := copier.Run(ctx, opts.copyOptions)
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
		line(stderr, "audit-history-copy:", err)
		return 1
	case report.Dropped > 0:
		line(stdout, fmt.Sprintf("verified; dropped %d partitions ending at or before %s", report.Dropped, report.Cutoff.UTC().Format(time.RFC3339)))
	default:
		line(stdout, "verified")
	}
	return 0
}
