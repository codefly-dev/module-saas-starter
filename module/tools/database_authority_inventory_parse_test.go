package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// The table parser is the half of the gate that reads prose, so its own failure
// modes are silent by construction: a relation it cannot see reads exactly like
// a relation the document never listed. These pin the two that bit.

const parseFixtureHeader = `# fixture

## Scope and RLS inventory

| Scope | Relations | Required database boundary |
|---|---|---|
`

func writeInventoryFixture(t *testing.T, rows string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "DATABASE_AUTHORITY.md")
	if err := os.WriteFile(path, []byte(parseFixtureHeader+rows+"\n## Next section\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// A Postgres identifier may contain digits. Matching only [a-z_] dropped such a
// relation from the documented set, and the gate then reported the document as
// missing a relation the document plainly listed.
func TestDocumentedRelationScopesReadsDigitsInRelationNames(t *testing.T) {
	path := writeInventoryFixture(t,
		"| `global` | `oauth2_clients`, `plans`, `s3_buckets` | No RLS; exact grants |\n")

	rows := documentedRelationScopes(t, path)
	row, ok := rows["global"]
	if !ok {
		t.Fatalf("scope global did not parse")
	}
	want := []string{"oauth2_clients", "plans", "s3_buckets"}
	if len(row.relations) != len(want) {
		t.Fatalf("relations = %v, want %v", row.relations, want)
	}
	for i, relation := range want {
		if row.relations[i] != relation {
			t.Errorf("relations[%d] = %q, want %q", i, row.relations[i], relation)
		}
	}
}

// The boundary is the last column, and it must survive parsing intact —
// including when an earlier column ends in a backquoted identifier.
func TestDocumentedRelationScopesReadsTheBoundaryColumn(t *testing.T) {
	path := writeInventoryFixture(t,
		"| `tenant` | `organizations` | Enabled and forced RLS with at least one policy |\n")

	row, ok := documentedRelationScopes(t, path)["tenant"]
	if !ok {
		t.Fatalf("scope tenant did not parse")
	}
	if row.boundary != "Enabled and forced RLS with at least one policy" {
		t.Errorf("boundary = %q", row.boundary)
	}
}

// A backquoted identifier in the boundary column is prose, not a relation.
func TestDocumentedRelationScopesIgnoresIdentifiersInTheBoundaryColumn(t *testing.T) {
	path := writeInventoryFixture(t,
		"| `global` | `plans` | No RLS; exact grants issued by `GRANT` |\n")

	row := documentedRelationScopes(t, path)["global"]
	if len(row.relations) != 1 || row.relations[0] != "plans" {
		t.Errorf("relations = %v, want [plans]", row.relations)
	}
}
