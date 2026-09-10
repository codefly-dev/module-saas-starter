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
// compares them relation-by-relation so the prose copy cannot drift again.
const (
	relationInventorySource = "services/accounts/code/pkg/infra/postgres_role_hardening_test.go"
	relationInventoryMap    = "relationsByScope"
	databaseAuthorityDoc    = "DATABASE_AUTHORITY.md"
	scopeInventoryHeading   = "## Scope and RLS inventory"
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
	code := executableRelationScopes(t, filepath.Join(moduleDir, filepath.FromSlash(relationInventorySource)))
	documented := documentedRelationScopes(t, filepath.Join(moduleDir, databaseAuthorityDoc))

	for scope, relations := range code {
		compareRelationSets(t, scope, relations, documented[scope])
	}
	for scope := range documented {
		if _, known := code[scope]; !known {
			t.Errorf("%s documents scope %q, which %s does not define", databaseAuthorityDoc, scope, relationInventoryMap)
		}
	}
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

// executableRelationScopes reads the scope map out of the accounts test source
// with the Go parser rather than a pattern, so a reformatting or a comment
// between entries cannot change what this test believes the code says.
func executableRelationScopes(t *testing.T, path string) map[string][]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
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

var (
	inventoryRow       = regexp.MustCompile(`^\|\s*` + "`" + `([a-z_]+)` + "`" + `\s*\|(.*)\|[^|]*\|$`)
	backquotedRelation = regexp.MustCompile("`([a-z_]+)`")
)

func documentedRelationScopes(t *testing.T, path string) map[string][]string {
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
	scopes := make(map[string][]string)
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "## ") {
			break
		}
		match := inventoryRow.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		for _, relation := range backquotedRelation.FindAllStringSubmatch(match[2], -1) {
			scopes[match[1]] = append(scopes[match[1]], relation[1])
		}
		sort.Strings(scopes[match[1]])
	}
	if len(scopes) == 0 {
		t.Fatalf("%s: the %q table parsed to no rows", path, scopeInventoryHeading)
	}
	return scopes
}
