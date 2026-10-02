package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every audit record in this service is written by the emitter, so that one
// switch — AUDIT_SINK — decides where every record goes (ADR 0009). A writer
// that inserts into audit_events itself keeps writing there under a swap
// value, where the store of record is elsewhere and audit_events receives
// nothing else: its records would reach neither the warehouse nor the archive.
//
// The SQL that writes an audit record lives in exactly one file, and the two
// store methods that run it are called from exactly one other.
func TestAuditRecordsAreWrittenOnlyThroughTheEmitter(t *testing.T) {
	const (
		sqlHome  = "pkg/infra/postgres_audit.go"
		callHome = "pkg/business/audit.go"
	)
	auditInsert := regexp.MustCompile(`(?i)\bINSERT\s+INTO\s+(?:public\.)?audit_event(?:s|_queue)\b`)

	var sqlWriters, callers []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == "pkg/gen" || strings.HasPrefix(entry.Name(), ".") && path != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if path != sqlHome && auditInsert.Match(source) {
			sqlWriters = append(sqlWriters, path)
		}
		parsed, err := parser.ParseFile(fset, path, source, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "InsertAuditEvent" && sel.Sel.Name != "EnqueueAuditEvent") {
				return true
			}
			if path != callHome {
				callers = append(callers, fset.Position(call.Pos()).String())
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	sort.Strings(sqlWriters)
	sort.Strings(callers)
	require.Empty(t, sqlWriters, "only %s may hold SQL that writes an audit record; route the others through the emitter (business.AuditRecorder)", sqlHome)
	require.Empty(t, callers, "only %s may call the store's audit record writes; route the others through the emitter (business.AuditRecorder)", callHome)
}
