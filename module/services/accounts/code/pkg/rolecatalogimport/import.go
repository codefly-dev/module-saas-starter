// Package rolecatalogimport syncs an external permission catalog into the
// built-in scoped roles (roles.built_in = true, org_id IS NULL). It upserts the
// catalog's roles and diff-applies their permissions; it never touches
// org-defined custom roles and never deletes assignments unless -force.
//
// Usage:
//
//	role-catalog-import -catalog roles.json [-database-url "$DATABASE_URL"] [-audit-sink postgres|both|bigquery|clickhouse] [-dry-run] [-force]
//
// Without an explicit URL it uses the accounts deployment's resolved Postgres
// capabilities and rotating credentials. An operator URL keeps its embedded
// credential and must have membership in app_tenant and app_control_plane;
// built-in roles cannot be written without the control-plane capability.
// See AUTHZ.md ("Built-in role catalog import") for the format and workflow.
//
// The import must run under the deployment's AUDIT_SINK, read through the same
// auditsink package the accounts service reads it with (-audit-sink, else
// $AUDIT_SINK): under a swap value (ADR 0009) the import's audit events go to
// the transactional queue, which the accounts relay delivers, rather than to
// audit_events. Unlike the service, the import has no default: a deploy step that
// did not receive the setting stops, because guessing postgres would write its
// events where a warehouse deployment never reads them.
package rolecatalogimport

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"accounts/pkg/auditops"
	"accounts/pkg/auditstore/auditsink"
	"accounts/pkg/business"
	"accounts/pkg/infra"
	"accounts/pkg/rolecatalog"
)

// Writes to the process's stdout/stderr have no meaningful recovery, so these
// helpers ignore the write error while keeping the io.Writer seam that lets the
// outcome/exit mapping be tested without a database.
func line(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }
func text(w io.Writer, s string) { _, _ = fmt.Fprint(w, s) }

// Run parses flags, applies the catalog, and returns a process exit code.
// Returning (rather than calling os.Exit inside) keeps every deferred cleanup —
// notably the store's connection pool — unwinding on all paths.
func Run(args []string, stdout, stderr io.Writer) int {
	return runWithOpeners(args, stdout, stderr, storeOpeners{
		deployed: auditops.OpenDeployedStore,
		explicit: infra.NewPostgresStoreFromURL,
	})
}

type storeOpeners struct {
	deployed func(context.Context) (*infra.PostgresStore, error)
	explicit func(context.Context, string) (*infra.PostgresStore, error)
}

func runWithOpeners(args []string, stdout, stderr io.Writer, openers storeOpeners) int {
	fs := flag.NewFlagSet("role-catalog-import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	catalogPath := fs.String("catalog", "", "path to the JSON permission catalog (required)")
	databaseURL := fs.String("database-url", os.Getenv("DATABASE_URL"), "operator Postgres URL (defaults to $DATABASE_URL; otherwise uses deployed accounts capabilities)")
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
	explicitFlag := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "database-url" {
			explicitFlag = true
		}
	})
	if explicitFlag && strings.TrimSpace(*databaseURL) == "" {
		line(stderr, "role-catalog-import: an explicit -database-url must not be blank")
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
	var store *infra.PostgresStore
	if strings.TrimSpace(*databaseURL) != "" {
		store, err = openers.explicit(ctx, *databaseURL)
	} else {
		store, err = openers.deployed(ctx)
	}
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
