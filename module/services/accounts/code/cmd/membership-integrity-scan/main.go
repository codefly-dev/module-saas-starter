// Command membership-integrity-scan re-runs the organization
// administrative-continuity inventory (migration 132) and prints the current
// backlog. It repairs nothing: both findings describe a state whose only
// resolution is an operator deciding who should hold authority, and a tool that
// picked for them would be performing a privilege escalation.
//
// Usage:
//
//	membership-integrity-scan -database-url "$DATABASE_URL" [-list-only]
//
// The connection principal must be a member of app_control_plane (the same
// authority migrations run under). That is not a formality: organizations,
// organization_members and the findings table all force row-level security,
// which binds the table owner as well, so a principal that does not span
// organizations reads zero rows everywhere. The scan refuses to run for such a
// principal rather than reporting an empty platform, and this command surfaces
// that refusal instead of printing a reassuring zero.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"accounts/pkg/infra"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// Writes to the process's stdout/stderr have no meaningful recovery, so these
// helpers ignore the write error while keeping the io.Writer seam that lets the
// outcome/exit mapping be tested without a database.
func line(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }

// run parses flags, scans, and returns a process exit code. Returning (rather
// than calling os.Exit inside) keeps every deferred cleanup — notably the
// store's connection pool — unwinding on all paths.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("membership-integrity-scan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	databaseURL := fs.String("database-url", os.Getenv("DATABASE_URL"), "Postgres connection URL (defaults to $DATABASE_URL)")
	listOnly := fs.Bool("list-only", false, "print the backlog recorded by the last scan without re-running it")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *databaseURL == "" {
		line(stderr, "membership-integrity-scan: -database-url (or $DATABASE_URL) is required")
		return 1
	}

	ctx := context.Background()
	store, err := infra.NewPostgresStoreFromURL(ctx, *databaseURL)
	if err != nil {
		line(stderr, "membership-integrity-scan:", err)
		return 1
	}
	defer store.Close()

	if !*listOnly {
		outstanding, err := store.RecordMembershipIntegrityFindings(ctx)
		if err != nil {
			line(stderr, "membership-integrity-scan:", err)
			return 1
		}
		// The two findings are mutually exclusive, so this counts
		// organizations, not rows about organizations.
		line(stdout, fmt.Sprintf("%d organizations outstanding", outstanding))
	}

	findings, err := store.ListMembershipIntegrityFindings(ctx)
	if err != nil {
		line(stderr, "membership-integrity-scan:", err)
		return 1
	}
	for _, finding := range findings {
		line(stdout, fmt.Sprintf("%s\t%s\t%s", finding.OrgID, finding.Finding, string(finding.Detail)))
	}
	return 0
}
