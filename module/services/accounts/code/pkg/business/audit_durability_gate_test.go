package business

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The durability gate. Every registered audit event declares whether it records
// a privileged write (DurabilityTransactional) or an observation
// (DurabilityObservational); this test reads the package's own source and
// asserts each emit site uses the path its classification demands:
//
//   - a transactional event never goes through s.emit, which commits on a
//     transaction of the emitter's own and would leave a security change with no
//     record when the process dies between the two commits;
//   - an observational event never goes through s.emitTx, which would make a
//     denial or an authentication outcome disappear with the domain write it was
//     observed under.
//
// It is deliberately structural rather than a scan for event strings: a new
// security mutation cannot pass by declaring an event, because the registry has
// no durability-less constructor and this test reads the classification, not the
// declaration.
//
// emitEntryTx sites are outside its reach by construction — they carry a
// pre-built entry whose type is often caller-supplied (the module-facing
// EmitAuditEvent surface) — but they are already on the transactional path, so
// what the gate cannot see there cannot be the failure it exists to catch.

// auditEmitExemptions are emit sites that a transactional classification cannot
// reach, keyed by "<file>:<function>". Each is a known, named hole in
// mutation/audit atomicity — not a waiver of the requirement — and the reason is
// what a reader should see when asking why the guarantee does not hold there.
var auditEmitExemptions = map[string]string{
	"invitations.go:announceInvitationAccepted": "the invitation is accepted inside the identity resolver's own transaction, which the business layer does not hold; this announcement runs after that commit, so its record cannot join it. Closing it means moving acceptance emission into the resolver.",
}

// unresolvedAuditEmitSites are emit sites whose event type is not a registered
// constant the gate can resolve. Each must be justified: an unresolvable type is
// a classification the gate cannot check.
var unresolvedAuditEmitSites = map[string]string{
	"waitlist.go:ReviewWaitlist": "the event type is composed from the target state; every saas.waitlist.* event is observational, so no transactional classification can hide here.",
}

type auditEmitSite struct {
	file     string
	function string
	line     int
	method   string // emit | emitTx
	event    EventType
	resolved bool
}

func TestAuditDurability_EveryRegisteredEventIsClassified(t *testing.T) {
	for _, d := range AuditEventCatalog() {
		require.Contains(t,
			[]AuditDurability{DurabilityTransactional, DurabilityObservational},
			d.Durability,
			"event %q must declare a durability", d.Type)
	}
}

func TestAuditDurability_EmitSitesMatchTheirClassification(t *testing.T) {
	sites := collectAuditEmitSites(t)
	require.NotEmpty(t, sites, "the gate found no emit sites at all; the AST walk is broken")

	usedExemptions := map[string]bool{}
	usedUnresolved := map[string]bool{}
	var violations []string

	for _, site := range sites {
		key := site.file + ":" + site.function
		if !site.resolved {
			if _, ok := unresolvedAuditEmitSites[key]; ok {
				usedUnresolved[key] = true
				continue
			}
			violations = append(violations, fmt.Sprintf(
				"%s:%d (%s): audit event expression is not a registered event constant, so its durability cannot be checked; resolve it or record it in unresolvedAuditEmitSites with a reason",
				site.file, site.line, site.function))
			continue
		}
		transactional := IsTransactionalAuditEvent(site.event)
		switch {
		case site.method == "emit" && transactional:
			if _, ok := auditEmitExemptions[key]; ok {
				usedExemptions[key] = true
				continue
			}
			violations = append(violations, fmt.Sprintf(
				"%s:%d (%s): %q is a transactional (security-write) event but is emitted with s.emit, which commits on its own transaction; use s.emitTx inside the mutation's transaction",
				site.file, site.line, site.function, site.event))
		case site.method == "emitTx" && !transactional:
			violations = append(violations, fmt.Sprintf(
				"%s:%d (%s): %q is an observational event but is emitted with s.emitTx, so it would be rolled back with the domain write it observes; use s.emit",
				site.file, site.line, site.function, site.event))
		}
	}

	sort.Strings(violations)
	require.Empty(t, violations, "audit durability violations:\n%s", strings.Join(violations, "\n"))

	for key := range auditEmitExemptions {
		require.True(t, usedExemptions[key],
			"stale exemption %q: no transactional s.emit site there any more — delete the entry", key)
	}
	for key := range unresolvedAuditEmitSites {
		require.True(t, usedUnresolved[key],
			"stale unresolved-site entry %q: the event expression resolves now — delete the entry", key)
	}
}

// collectAuditEmitSites parses the package's own non-test sources and returns
// every s.emit / s.emitTx call with the event constant it names.
func collectAuditEmitSites(t *testing.T) []auditEmitSite {
	t.Helper()

	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var files []*ast.File
	names := map[*ast.File]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		require.NoError(t, err, "parse %s", name)
		files = append(files, parsed)
		names[parsed] = name
	}

	constants := auditEventConstants(files)
	require.NotEmpty(t, constants, "no EventType constants found; the registry parse is broken")

	var sites []auditEmitSite
	for _, file := range files {
		var function string
		var locals map[string][]EventType
		ast.Inspect(file, func(n ast.Node) bool {
			if decl, ok := n.(*ast.FuncDecl); ok {
				function = decl.Name.Name
				locals = eventLocals(decl, constants)
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
			receiver, ok := sel.X.(*ast.Ident)
			if !ok || receiver.Name != "s" {
				return true
			}
			if sel.Sel.Name != "emit" && sel.Sel.Name != "emitTx" {
				return true
			}
			// emit(ctx, actorID, actorType, eventType, ...)
			if len(call.Args) < 4 {
				return true
			}
			site := auditEmitSite{
				file:     names[file],
				function: function,
				line:     fset.Position(call.Pos()).Line,
				method:   sel.Sel.Name,
			}
			if ident, ok := call.Args[3].(*ast.Ident); ok {
				if event, known := constants[ident.Name]; known {
					site.event, site.resolved = event, true
				} else if assigned, known := locals[ident.Name]; known {
					// A local holding one of several event constants (an
					// approve/deny branch, say) resolves only when every value it
					// can hold agrees on durability — otherwise one branch would
					// be checked against the other's rule.
					if event, agreed := sameDurability(assigned); agreed {
						site.event, site.resolved = event, true
					}
				}
			}
			sites = append(sites, site)
			return true
		})
	}
	return sites
}

// eventLocals maps each local variable in fn that is only ever assigned audit
// event constants to the set of constants it can hold.
func eventLocals(fn *ast.FuncDecl, constants map[string]EventType) map[string][]EventType {
	out := map[string][]EventType{}
	tainted := map[string]bool{}
	record := func(lhs, rhs []ast.Expr) {
		for i, target := range lhs {
			name, ok := target.(*ast.Ident)
			if !ok || i >= len(rhs) {
				continue
			}
			source, ok := rhs[i].(*ast.Ident)
			if !ok {
				tainted[name.Name] = true
				continue
			}
			event, known := constants[source.Name]
			if !known {
				tainted[name.Name] = true
				continue
			}
			out[name.Name] = append(out[name.Name], event)
		}
	}
	ast.Inspect(fn, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok {
			record(assign.Lhs, assign.Rhs)
		}
		return true
	})
	for name := range tainted {
		delete(out, name)
	}
	return out
}

// sameDurability reports the shared classification of a set of event types.
func sameDurability(events []EventType) (EventType, bool) {
	if len(events) == 0 {
		return "", false
	}
	want := IsTransactionalAuditEvent(events[0])
	for _, event := range events[1:] {
		if IsTransactionalAuditEvent(event) != want {
			return "", false
		}
	}
	return events[0], true
}

// auditEventConstants maps each `EventFoo EventType = "saas.…"` constant name to
// its value, so an emit site's identifier can be resolved to a registered type.
func auditEventConstants(files []*ast.File) map[string]EventType {
	out := map[string]EventType{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
					continue
				}
				typeName, ok := value.Type.(*ast.Ident)
				if !ok || typeName.Name != "EventType" {
					continue
				}
				literal, ok := value.Values[0].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				unquoted, err := strconv.Unquote(literal.Value)
				if err != nil {
					continue
				}
				out[value.Names[0].Name] = EventType(unquoted)
			}
		}
	}
	return out
}
