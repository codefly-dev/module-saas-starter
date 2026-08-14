// Command role-catalog-import syncs an external permission catalog into the
// built-in scoped roles (roles.built_in = true, org_id IS NULL). It upserts the
// catalog's roles and diff-applies their permissions; it never touches
// org-defined custom roles and never deletes assignments unless -force.
//
// Usage:
//
//	role-catalog-import -catalog roles.json -database-url "$DATABASE_URL" [-dry-run] [-force]
//
// The connection principal must be a member of app_control_plane (the same
// authority migrations run under); built-in roles cannot be written otherwise.
// See AUTHZ.md ("Built-in role catalog import") for the format and workflow.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

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

	result, err := store.ImportRoleCatalog(ctx, catalog, infra.ImportOptions{DryRun: *dryRun, Force: *force, Source: *catalogPath})
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
