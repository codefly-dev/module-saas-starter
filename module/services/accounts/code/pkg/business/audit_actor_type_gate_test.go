package business

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The actor-type gate. audit_events.actor_type is a CHECK-constrained column
// ('user', 'api_key', 'system', 'agent'); every emit site names one, and most
// name it with a bare string literal rather than the registered constant. That
// makes the vocabulary a convention rather than a type, and a value outside it
// — "apikey", "service" — compiles, passes review, and is rejected only by
// Postgres at INSERT. Because emitTx writes on the caller's transaction, the
// rejection does not just lose the record: it fails the mutation the record
// describes, at runtime, in production.
//
// This test reads the package's own source and requires every literal actor
// type passed to s.emit / s.emitTx to be one of the four. It is the gate that
// caught actorTypeForCreator returning "service" for a service principal.
//
// A non-literal argument (a variable or a struct field) cannot be resolved
// here, so each is listed in auditActorTypeIndirectSites with the runtime
// guarantee that stands in for the check — an unexplained indirection fails.
// A value assigned from actorTypeForCreator resolves without an entry here:
// that helper returns a registered constant on every branch, and
// TestActorTypeForCreator_SpeaksTheRegisteredVocabulary pins it. Resolving the
// helper rather than listing its call sites keeps a new caller from failing
// this gate for a reason that is already guaranteed.
var auditActorTypeIndirectSites = map[string]string{
	"webhooks.go:CreateSubscription":    "AuditActor.Type, rejected by AuditActor.validate before the mutation opens its transaction.",
	"webhooks.go:DeleteSubscription":    "AuditActor.Type, rejected by AuditActor.validate before the mutation opens its transaction.",
	"webhooks.go:ReplayWebhookDelivery": "AuditActor.Type, rejected by AuditActor.validate before the mutation opens its transaction.",
	"webhooks.go:RotateWebhookSecret":   "AuditActor.Type, rejected by AuditActor.validate before the mutation opens its transaction.",
}

func TestAuditActorTypes_AreTheValuesTheColumnAdmits(t *testing.T) {
	registered := map[string]bool{
		ActorTypeUser:   true,
		ActorTypeAPIKey: true,
		ActorTypeSystem: true,
		ActorTypeAgent:  true,
	}
	// The constants must themselves match the column's CHECK. Reading the
	// migration keeps this from being a check of the code against itself.
	checkSQL, err := os.ReadFile(filepath.Join(auditMigrationsDir(t), "97_audit_events_typed_partitioned.up.sql"))
	require.NoError(t, err)
	for value := range registered {
		require.Contains(t, string(checkSQL), "'"+value+"'",
			"actor type %q is not admitted by the audit_events.actor_type CHECK constraint", value)
	}

	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var offenders []string
	var unexplained []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		require.NoError(t, err, "parse %s", name)

		var function string
		var derived map[string]bool
		ast.Inspect(file, func(n ast.Node) bool {
			if decl, ok := n.(*ast.FuncDecl); ok {
				function = decl.Name.Name
				derived = actorTypeLocals(decl)
				return true
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, ok := sel.X.(*ast.Ident); !ok {
				return true
			}
			if sel.Sel.Name != "emit" && sel.Sel.Name != "emitTx" {
				return true
			}
			// emit(ctx, actorID, actorType, eventType, ...)
			if len(call.Args) < 4 {
				return true
			}
			site := name + ":" + function
			switch arg := call.Args[2].(type) {
			case *ast.BasicLit:
				if arg.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(arg.Value)
				if err != nil {
					return true
				}
				if !registered[value] {
					offenders = append(offenders, site+" emits actor_type "+strconv.Quote(value))
				}
			case *ast.Ident:
				// A registered constant resolves, and so does a local the
				// function took from actorTypeForCreator; anything else is
				// indirection this gate cannot see through.
				if registered[identConstantValue(arg.Name)] || derived[arg.Name] {
					return true
				}
				if _, explained := auditActorTypeIndirectSites[site]; !explained {
					unexplained = append(unexplained, site+" (identifier "+arg.Name+")")
				}
			default:
				if _, explained := auditActorTypeIndirectSites[site]; !explained {
					unexplained = append(unexplained, site+" (non-literal actor type)")
				}
			}
			return true
		})
	}

	require.Empty(t, offenders,
		"audit_events.actor_type only admits %q, %q, %q, %q; a value outside that set is rejected by the CHECK "+
			"constraint at INSERT, and on the emitTx path that failure aborts the mutation it was recording",
		ActorTypeUser, ActorTypeAPIKey, ActorTypeSystem, ActorTypeAgent)
	require.Empty(t, unexplained,
		"an emit site whose actor type this gate cannot resolve must name the runtime guarantee that replaces the check, in auditActorTypeIndirectSites")
}

// actorTypeLocals collects the identifiers a function assigns from
// actorTypeForCreator, whose every branch returns a registered constant.
func actorTypeLocals(decl *ast.FuncDecl) map[string]bool {
	locals := map[string]bool{}
	ast.Inspect(decl, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "actorTypeForCreator" {
			return true
		}
		if name, ok := assign.Lhs[0].(*ast.Ident); ok {
			locals[name.Name] = true
		}
		return true
	})
	return locals
}

// auditMigrationsDir locates the store migrations by walking up from this test
// file until it finds them. Anchoring on the file (rather than the working
// directory) and searching (rather than hard-coding how deep this package sits)
// keeps the gate from silently passing its own path arithmetic instead of the
// CHECK constraint.
func auditMigrationsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "cannot locate this test file")
	dir := filepath.Dir(thisFile)
	for range 12 {
		candidate := filepath.Join(dir, "services", "store", "migrations")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("cannot find services/store/migrations above %s", filepath.Dir(thisFile))
	return ""
}

// identConstantValue maps an ActorType* identifier back to its value, so a site
// that already uses the registered constant resolves.
func identConstantValue(name string) string {
	switch name {
	case "ActorTypeUser":
		return ActorTypeUser
	case "ActorTypeAPIKey":
		return ActorTypeAPIKey
	case "ActorTypeSystem":
		return ActorTypeSystem
	case "ActorTypeAgent":
		return ActorTypeAgent
	}
	return ""
}
