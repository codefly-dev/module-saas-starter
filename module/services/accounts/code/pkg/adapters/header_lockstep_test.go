package adapters

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// trustControlHeaders are read by the interceptor to establish trust rather than
// to carry an identity claim, so they are not part of the forwarded-identity
// strip set: X-Codefly-Gateway-Token is validated then deleted unconditionally,
// X-Codefly-Public-Origin is deleted unconditionally, and Authorization is the
// caller's own bearer credential.
var trustControlHeaders = map[string]bool{
	"X-Codefly-Gateway-Token": true,
	"X-Codefly-Public-Origin": true,
	"Authorization":           true,
}

// TestUntrustedHeaders_SupersetOfTrustedHeaders is the accounts-side half of the
// header-lockstep gate. connect_auth_interceptor.go trusts a set of forwarded
// identity headers when the request carries a valid gateway token, and strips
// that same set (forwardedIdentityHeaders) when it does not. If a header the
// interceptor reads is missing from the strip set, an untrusted caller reaching
// the API without the sidecar could smuggle it in — the exact privilege
// escalation the strip is there to prevent.
//
// The scan is AST-based: it collects every string literal passed to a
// headers.Get(...) call in the interceptor and asserts each is either a
// trust-control header or a member of forwardedIdentityHeaders. Adding a new
// headers.Get("X-...") read without extending forwardedIdentityHeaders fails
// here, keeping the trusted set and the stripped set in lockstep.
func TestUntrustedHeaders_SupersetOfTrustedHeaders(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Join(filepath.Dir(thisFile), "connect_auth_interceptor.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, source, nil, 0)
	require.NoError(t, err)

	strip := make(map[string]bool, len(forwardedIdentityHeaders))
	for _, h := range forwardedIdentityHeaders {
		strip[h] = true
	}

	var offenders []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Get" || len(call.Args) != 1 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		header, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if !strings.HasPrefix(header, "X-") && header != "Authorization" {
			return true
		}
		if trustControlHeaders[header] || strip[header] {
			return true
		}
		offenders = append(offenders, header)
		return true
	})

	require.Empty(t, offenders,
		"every identity header the interceptor trusts must be in forwardedIdentityHeaders so it is stripped from callers that arrive without a valid gateway token")
}
