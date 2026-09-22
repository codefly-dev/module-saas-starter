package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// Both of the gate's parsers fail silently by construction: a relation either
// one cannot see reads exactly like a relation the other side never listed.
// These pin the modes that bit — the prose table's first, then the Go
// inventory's, whose entries carry their scope as a field rather than as a map
// key.

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

const inventoryFixture = `package relationcatalog

type Scope string

const (
	ScopeGlobal Scope = "global"
	ScopeTenant Scope = "tenant"
	ScopeUser   Scope = "user"
)

func (s Scope) RequiresRLS() bool {
	switch s {
	case ScopeTenant, ScopeUser:
		return true
	default:
		return false
	}
}

var authorities = map[string]Authority{
	"plans":         {Scope: ScopeGlobal},
	"organizations": {Scope: ScopeTenant, PolicyShape: ShapeSelfReferential, ScopeColumn: "id"},
	"api_keys":      {Scope: ScopeTenant, PolicyShape: ShapeDirect, ScopeColumn: "organization_id"},
	"sessions":      {Scope: ScopeUser, PolicyShape: ShapeDirect, ScopeColumn: "user_id"},
}
`

func parseInventoryFixture(t *testing.T) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "catalog.go", inventoryFixture, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return file
}

// The inventory is keyed by relation with the scope as a struct field, so the
// reader has to group by that field. Reading map keys instead would yield the
// relation names as scopes and every real scope as empty.
func TestExecutableRelationScopesGroupsEntriesByTheirScopeField(t *testing.T) {
	scopes := executableRelationScopes(t, parseInventoryFixture(t))

	want := map[string][]string{
		"global": {"plans"},
		"tenant": {"api_keys", "organizations"},
		"user":   {"sessions"},
	}
	if len(scopes) != len(want) {
		t.Fatalf("scopes = %v, want %v", scopes, want)
	}
	for scope, relations := range want {
		got := scopes[scope]
		if len(got) != len(relations) {
			t.Fatalf("scope %q = %v, want %v", scope, got, relations)
		}
		for i, relation := range relations {
			if got[i] != relation {
				t.Errorf("scope %q relation[%d] = %q, want %q", scope, i, got[i], relation)
			}
		}
	}
}

// Only the cases that answer true require RLS. Collecting every scope constant
// in the method — the shape the predicate had when it was a boolean expression
// — would claim the scopes it deliberately excludes.
func TestRLSRequiringScopesReadsOnlyTheCasesThatRequireIt(t *testing.T) {
	scopes := rlsRequiringScopes(t, parseInventoryFixture(t))

	if !scopes["tenant"] || !scopes["user"] {
		t.Errorf("scopes = %v, want tenant and user", scopes)
	}
	if scopes["global"] {
		t.Error("global answers false in the switch but was read as requiring RLS")
	}
	if len(scopes) != 2 {
		t.Errorf("scopes = %v, want exactly tenant and user", scopes)
	}
}
