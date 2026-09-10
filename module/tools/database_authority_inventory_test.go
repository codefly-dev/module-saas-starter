package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The relation inventory exists twice: once as the executable map the accounts
// infrastructure suite checks a live database against, and once as the table a
// reader of DATABASE_AUTHORITY.md consults to learn which boundary a relation
// requires. The second copy is the one nothing enforced — it still listed
// audit_export_configs (dropped in store migration 102), knew nothing of the
// worker scope, and named eleven of the thirty-eight tenant relations. This
// compares them relation-by-relation so the prose copy cannot drift again, and
// compares each scope's documented boundary against the RLS verdict the
// live-database check derives from that same scope — the boundary being the
// half a reader actually acts on.
const (
	relationInventorySource = "services/accounts/code/pkg/infra/postgres_role_hardening_test.go"
	relationInventoryMap    = "relationsByScope"
	// rlsPredicateVar is the local the live-database check assigns the required
	// RLS verdict to, per scope. Reading the scope set out of that expression
	// is what keeps the documented boundary column derived rather than a second
	// hand-maintained copy.
	rlsPredicateVar       = "requiresRLS"
	databaseAuthorityDoc  = "DATABASE_AUTHORITY.md"
	scopeInventoryHeading = "## Scope and RLS inventory"
)

// scopeConstantValues maps the relationScope constant identifiers used as keys
// in the executable map to the scope names the documented table uses.
var scopeConstantValues = map[string]string{
	"relationScopeGlobal":  "global",
	"relationScopeTenant":  "tenant",
	"relationScopeUser":    "user",
	"relationScopePreAuth": "pre_auth",
	"relationScopeJob":     "job",
	"relationScopeWorker":  "worker",
}

func TestDatabaseAuthorityScopeInventoryMatchesCode(t *testing.T) {
	moduleDir := findModuleDir(t)
	source := parseInventorySource(t, filepath.Join(moduleDir, filepath.FromSlash(relationInventorySource)))
	code := executableRelationScopes(t, source)
	documented := documentedRelationScopes(t, filepath.Join(moduleDir, databaseAuthorityDoc))

	for scope, relations := range code {
		compareRelationSets(t, scope, relations, documented[scope].relations)
	}
	for scope := range documented {
		if _, known := code[scope]; !known {
			t.Errorf("%s documents scope %q, which %s does not define", databaseAuthorityDoc, scope, relationInventoryMap)
		}
	}
}

// The relation names alone are the cheap half. What a reader of the table acts
// on is the boundary column, and nothing checked it: a scope's row could claim
// "No RLS" for relations the live-database check requires forced RLS on, and
// stay green. Derive the RLS verdict per scope from that check's own predicate.
func TestDatabaseAuthorityBoundaryMatchesCode(t *testing.T) {
	moduleDir := findModuleDir(t)
	source := parseInventorySource(t, filepath.Join(moduleDir, filepath.FromSlash(relationInventorySource)))
	rlsRequired := rlsRequiringScopes(t, source)
	documented := documentedRelationScopes(t, filepath.Join(moduleDir, databaseAuthorityDoc))

	for scope, row := range documented {
		boundary := strings.ToLower(row.boundary)
		if rlsRequired[scope] {
			if !strings.Contains(boundary, "forced rls") {
				t.Errorf("scope %q: %s requires forced RLS, %s says %q", scope, relationInventorySource, databaseAuthorityDoc, row.boundary)
			}
			continue
		}
		if strings.Contains(boundary, "forced rls") {
			t.Errorf("scope %q: %s does not require RLS, %s claims forced RLS (%q)", scope, relationInventorySource, databaseAuthorityDoc, row.boundary)
		}
		if !strings.Contains(boundary, "no rls") {
			t.Errorf("scope %q: %s does not require RLS; %s must say so outright, not %q", scope, relationInventorySource, databaseAuthorityDoc, row.boundary)
		}
	}
	for scope := range rlsRequired {
		if _, known := documented[scope]; !known {
			t.Errorf("%s requires RLS for scope %q, which %s does not document", relationInventorySource, scope, databaseAuthorityDoc)
		}
	}
}

// rlsRequiringScopes reads the scope set out of the live-database check's own
// `requiresRLS := scope == relationScopeX || ...` assignment, so the documented
// boundary is derived from the assertion rather than restated beside it.
func rlsRequiringScopes(t *testing.T, file *ast.File) map[string]bool {
	t.Helper()
	scopes := make(map[string]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		if name, ok := assign.Lhs[0].(*ast.Ident); !ok || name.Name != rlsPredicateVar {
			return true
		}
		ast.Inspect(assign.Rhs[0], func(inner ast.Node) bool {
			if identifier, ok := inner.(*ast.Ident); ok {
				if scope, known := scopeConstantValues[identifier.Name]; known {
					scopes[scope] = true
				}
			}
			return true
		})
		return false
	})
	if len(scopes) == 0 {
		t.Fatalf("%s: found no scope constants in the %s predicate", relationInventorySource, rlsPredicateVar)
	}
	return scopes
}

func compareRelationSets(t *testing.T, scope string, code, documented []string) {
	t.Helper()
	inDoc := make(map[string]bool, len(documented))
	for _, relation := range documented {
		inDoc[relation] = true
	}
	inCode := make(map[string]bool, len(code))
	for _, relation := range code {
		inCode[relation] = true
	}
	for _, relation := range code {
		if !inDoc[relation] {
			t.Errorf("scope %q: %s lists %q, %s does not", scope, relationInventoryMap, relation, databaseAuthorityDoc)
		}
	}
	for _, relation := range documented {
		if !inCode[relation] {
			t.Errorf("scope %q: %s lists %q, %s does not", scope, databaseAuthorityDoc, relation, relationInventoryMap)
		}
	}
}

// parseInventorySource reads the accounts test source with the Go parser rather
// than a pattern, so a reformatting or a comment between entries cannot change
// what these tests believe the code says.
func parseInventorySource(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

// executableRelationScopes reads the scope map out of the parsed source.
func executableRelationScopes(t *testing.T, file *ast.File) map[string][]string {
	t.Helper()
	path := relationInventorySource
	literal := findMapLiteral(file, relationInventoryMap)
	if literal == nil {
		t.Fatalf("%s does not declare %s as a map literal", path, relationInventoryMap)
	}
	scopes := make(map[string][]string, len(literal.Elts))
	for _, element := range literal.Elts {
		entry, ok := element.(*ast.KeyValueExpr)
		if !ok {
			t.Fatalf("%s: unexpected entry in %s", path, relationInventoryMap)
		}
		key, ok := entry.Key.(*ast.Ident)
		if !ok {
			t.Fatalf("%s: %s is keyed by something other than a scope constant", path, relationInventoryMap)
		}
		scope, known := scopeConstantValues[key.Name]
		if !known {
			t.Fatalf("%s: unknown scope constant %s; add it to scopeConstantValues", path, key.Name)
		}
		relations, ok := entry.Value.(*ast.CompositeLit)
		if !ok {
			t.Fatalf("%s: scope %s is not a slice literal", path, scope)
		}
		for _, relation := range relations.Elts {
			name, ok := relation.(*ast.BasicLit)
			if !ok || name.Kind != token.STRING {
				t.Fatalf("%s: scope %s holds a non-literal relation name", path, scope)
			}
			unquoted, err := strconv.Unquote(name.Value)
			if err != nil {
				t.Fatalf("%s: scope %s holds an unparsable relation name %s", path, scope, name.Value)
			}
			scopes[scope] = append(scopes[scope], unquoted)
		}
		sort.Strings(scopes[scope])
	}
	return scopes
}

func findMapLiteral(file *ast.File, name string) *ast.CompositeLit {
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.VAR {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, identifier := range value.Names {
				if identifier.Name != name || i >= len(value.Values) {
					continue
				}
				if literal, ok := value.Values[i].(*ast.CompositeLit); ok {
					return literal
				}
			}
		}
	}
	return nil
}

// A relation name is a Postgres identifier, which admits digits: matching only
// [a-z_] silently dropped a name like `oauth2_clients` from the documented set
// and then reported the table as missing a relation the table listed.
var (
	scopeCell          = regexp.MustCompile("^`([a-z0-9_]+)`$")
	backquotedRelation = regexp.MustCompile("`([a-z0-9_]+)`")
)

// documentedRow is one row of the scope inventory: the relations it names and
// the boundary it claims for them.
type documentedRow struct {
	relations []string
	boundary  string
}

// tableCells splits a markdown table row into its cells. Splitting beats a
// whole-row pattern here: the previous regex consumed the middle columns with a
// greedy group anchored on the last two pipes, so it silently changed meaning
// if a column were ever added, and it could not expose the boundary column at
// all. Returns nil for anything that is not a body row.
func tableCells(line string) []string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
		return nil
	}
	parts := strings.Split(strings.Trim(trimmed, "|"), "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}
	return cells
}

func documentedRelationScopes(t *testing.T, path string) map[string]documentedRow {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(body), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == scopeInventoryHeading {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s has no %q section", path, scopeInventoryHeading)
	}
	scopes := make(map[string]documentedRow)
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "## ") {
			break
		}
		cells := tableCells(line)
		if len(cells) == 0 {
			continue
		}
		scopeMatch := scopeCell.FindStringSubmatch(cells[0])
		if scopeMatch == nil {
			// The header and its separator, and any prose line that happens to
			// start with a pipe.
			continue
		}
		if len(cells) != 3 {
			t.Fatalf("%s: scope row %q has %d columns, expected scope / relations / boundary", path, cells[0], len(cells))
		}
		scope := scopeMatch[1]
		if _, seen := scopes[scope]; seen {
			t.Fatalf("%s: scope %q has more than one row", path, scope)
		}
		row := documentedRow{boundary: cells[2]}
		for _, relation := range backquotedRelation.FindAllStringSubmatch(cells[1], -1) {
			row.relations = append(row.relations, relation[1])
		}
		if len(row.relations) == 0 {
			t.Fatalf("%s: scope %q names no relation", path, scope)
		}
		if row.boundary == "" {
			t.Fatalf("%s: scope %q names no required database boundary", path, scope)
		}
		sort.Strings(row.relations)
		scopes[scope] = row
	}
	if len(scopes) == 0 {
		t.Fatalf("%s: the %q table parsed to no rows", path, scopeInventoryHeading)
	}
	return scopes
}
