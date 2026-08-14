// Command role-catalog-import syncs an external permission catalog into the
// built-in scoped roles (roles.built_in = true, org_id IS NULL). It upserts the
// catalog's roles and diff-applies their permissions; it never touches
// org-defined custom roles and never deletes assignments unless --force.
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
	"os"

	"accounts/pkg/infra"
	"accounts/pkg/rolecatalog"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "role-catalog-import:", err)
		os.Exit(1)
	}
}

func run() error {
	catalogPath := flag.String("catalog", "", "path to the JSON permission catalog (required)")
	databaseURL := flag.String("database-url", os.Getenv("DATABASE_URL"), "Postgres connection URL (defaults to $DATABASE_URL)")
	dryRun := flag.Bool("dry-run", false, "print the plan without applying it")
	force := flag.Bool("force", false, "apply removals even when they would delete existing role assignments")
	flag.Parse()

	if *catalogPath == "" {
		return fmt.Errorf("-catalog is required")
	}
	if *databaseURL == "" {
		return fmt.Errorf("-database-url (or $DATABASE_URL) is required")
	}

	document, err := os.ReadFile(*catalogPath)
	if err != nil {
		return fmt.Errorf("read catalog: %w", err)
	}
	catalog, err := rolecatalog.Parse(document)
	if err != nil {
		return err
	}

	ctx := context.Background()
	store, err := infra.NewPostgresStoreFromURL(ctx, *databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	result, err := store.ImportRoleCatalog(ctx, catalog, infra.ImportOptions{DryRun: *dryRun, Force: *force})
	if err != nil {
		return err
	}

	fmt.Print(result.Plan.Format())

	switch {
	case *dryRun:
		fmt.Println("dry-run: no changes applied")
	case result.Refused:
		orphaned := result.Plan.OrphaningRemovals()
		fmt.Fprintf(os.Stderr, "refused: %d role removal(s) would orphan assignments; rerun with -force to apply\n", len(orphaned))
		os.Exit(2)
	case result.Applied && result.Plan.Empty():
		fmt.Println("no changes")
	default:
		fmt.Println("applied")
	}
	return nil
}
