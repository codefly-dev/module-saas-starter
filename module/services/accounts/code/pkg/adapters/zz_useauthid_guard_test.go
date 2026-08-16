package adapters

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// userAuthIDAllowlist names the files where reading wool's UserAuthID() directly
// is the canonical implementation, not a handler shortcut:
//   - auth.go: requireAuth / callerID — the helpers every handler routes through.
//   - connect_handlers.go: the Connect-layer callerID plumbing (UserID-first).
//   - rate_limit_interceptor.go: keys the request budget and guards id != "" itself.
//
// Every other file — every current and future RPC handler, whatever it is named —
// is scanned.
var userAuthIDAllowlist = map[string]bool{
	"auth.go":                   true,
	"connect_handlers.go":       true,
	"rate_limit_interceptor.go": true,
}

// TestGuard_NoRawUserAuthIDInRPCHandlers keeps handlers from reading the caller
// id straight off wool's UserAuthID() (X-Auth-Id / user.auth.id). Behind the
// Envoy auth-sidecar the gateway stamps only X-User-Id and forwards a blank
// user.auth.id, so a raw UserAuthID() read collapses to an empty actor that flows
// into uuid-typed SQL and 500s (see #121). Handlers must resolve the actor via
// requireAuth(ctx), which prefers UserID() and rejects empties.
//
// The scan is AST-based: it matches only genuine `.UserAuthID()` method calls, so
// it is blind to the identifier appearing in a comment (auth.go's doc block) or as
// the UserAuthIDKey constant (grpc_auth_interceptor.go), and it covers the whole
// package rather than a filename pattern that a new handler file could sidestep.
func TestGuard_NoRawUserAuthIDInRPCHandlers(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Dir(thisFile)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	fset := token.NewFileSet()
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if userAuthIDAllowlist[name] {
			continue
		}

		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		require.NoError(t, err)

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "UserAuthID" {
				offenders = append(offenders, fmt.Sprintf("%s:%d", name, fset.Position(call.Pos()).Line))
			}
			return true
		})
	}

	require.Empty(t, offenders,
		"RPC handlers must resolve the actor via requireAuth(ctx), not a raw w.UserAuthID() read (empty behind the auth-sidecar; see #121)")
}
