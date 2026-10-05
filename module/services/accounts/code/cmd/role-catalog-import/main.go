// Command role-catalog-import syncs an external permission catalog into the
// built-in scoped roles (roles.built_in = true, org_id IS NULL). It upserts the
// catalog's roles and diff-applies their permissions; it never touches
// org-defined custom roles and never deletes assignments unless -force.
//
// Usage:
//
//	role-catalog-import -catalog roles.json -database-url "$DATABASE_URL" [-audit-sink postgres|both|bigquery|clickhouse] [-dry-run] [-force]
//
// The connection principal must be a member of app_control_plane (the same
// authority migrations run under); built-in roles cannot be written otherwise.
// See AUTHZ.md ("Built-in role catalog import") for the format and workflow.
//
// The import must run under the deployment's AUDIT_SINK, read through the same
// auditsink package the accounts service reads it with (-audit-sink, else
// $AUDIT_SINK): under a swap value (ADR 0009) the import's audit events go to
// the transactional queue, which the accounts relay delivers, rather than to
// audit_events. Unlike the service, the import has no default: a deploy step that
// did not receive the setting stops, because guessing postgres would write its
// events where a warehouse deployment never reads them.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"accounts/pkg/auditstore/auditsink"
	"accounts/pkg/business"
	"accounts/pkg/infra"
	"accounts/pkg/rolecatalog"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// Writes to the process's stdout/stderr have no meaningful recovery, so these
// helpers ignore the write error while keeping the io.Writer seam that lets the
// outcome/exit mapping be tested without a database.
func line(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }
func text(w io.Writer, s string) { _, _ = fmt.Fprint(w, s) }

// run parses flags, applies the catalog, and returns a process exit code.
// Returning (rather than calling os.Exit inside) keeps every deferred cleanup —
// notably the store's connection pool — unwinding on all paths.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("role-catalog-import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	catalogPath := fs.String("catalog", "", "path to the JSON permission catalog (required)")
	databaseURL := fs.String("database-url", os.Getenv("DATABASE_URL"), "Postgres connection URL (defaults to $DATABASE_URL)")
	dryRun := fs.Bool("dry-run", false, "print the plan without applying it")
	force := fs.Bool("force", false, "apply removals even when they would delete assignments or wipe the whole catalog")
	auditSink := fs.String("audit-sink", "", "the deployment's AUDIT_SINK: postgres, both, bigquery or clickhouse (defaults to $AUDIT_SINK, which must then be set)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *catalogPath == "" {
		line(stderr, "role-catalog-import: -catalog is required")
		return 1
	}
	if *databaseURL == "" {
		line(stderr, "role-catalog-import: -database-url (or $DATABASE_URL) is required")
		return 1
	}

	// The flag, when given, stands in for the environment variable the service
	// reads; either way the value is read and checked by auditsink, and a value
	// that was never set is refused rather than defaulted.
	lookup := os.LookupEnv
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "audit-sink" {
			lookup = func(string) (string, bool) { return *auditSink, true }
		}
	})
	sinkMode, err := auditsink.RequireMode(lookup)
	if err != nil {
		line(stderr, "role-catalog-import:", err)
		return 1
	}

	document, err := os.ReadFile(*catalogPath)
	if err != nil {
		line(stderr, "role-catalog-import:", err)
		return 1
	}
	catalog, err := rolecatalog.Parse(document)
	if err != nil {
		line(stderr, "role-catalog-import:", err)
		return 1
	}

	ctx := context.Background()
	store, err := infra.NewPostgresStoreFromURL(ctx, *databaseURL)
	if err != nil {
		line(stderr, "role-catalog-import:", err)
		return 1
	}
	defer store.Close()

	var recorderOptions []business.DurableAuditEmitterOption
	if sinkMode.Swaps() {
		recorderOptions = append(recorderOptions, business.WithQueuedRecords())
	}
	// The import records platform events only (built-in roles have no
	// organization), which neither the tee nor webhook delivery carries, so
	// the recorder needs nothing from the mode but where the record goes.
	recorder, err := business.NewDurableAuditEmitter(store, store, recorderOptions...)
	if err != nil {
		line(stderr, "role-catalog-import:", err)
		return 1
	}

	result, err := store.ImportRoleCatalog(ctx, catalog, infra.ImportOptions{
		DryRun: *dryRun, Force: *force, Source: *catalogPath, Audit: recorder,
	})
	if err != nil {
		line(stderr, "role-catalog-import:", err)
		return 1
	}

	return report(stdout, stderr, result, *dryRun)
}

// report prints the plan and outcome and returns the exit code. Pure over its
// inputs so the outcome/exit mapping is testable without a database.
func report(stdout, stderr io.Writer, result *infra.ImportResult, dryRun bool) int {
	text(stdout, result.Plan.Format())
	switch {
	case dryRun:
		line(stdout, "dry-run: no changes applied")
		return 0
	case result.Refused:
		line(stderr, "refused:", result.RefusalReason)
		return 2
	case result.Applied && result.Plan.Empty():
		line(stdout, "no changes")
		return 0
	default:
		line(stdout, "applied")
		return 0
	}
}
