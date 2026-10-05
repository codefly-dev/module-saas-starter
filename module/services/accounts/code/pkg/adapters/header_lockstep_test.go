package adapters

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
// The scan is AST-based: it collects every header name passed to a
// headers.Get(...) call in the interceptor and asserts each is either a
// trust-control header or a member of forwardedIdentityHeaders. Adding a new
// headers.Get("X-...") read without extending forwardedIdentityHeaders fails
// here, keeping the trusted set and the stripped set in lockstep.
//
// A name reached through a package const counts as much as a literal. Reading
// one through a const otherwise walked straight past this gate, which is the
// one escape it cannot afford: the whole point is that a header the interceptor
// trusts is a header the interceptor strips.
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

	// Package-level string consts in this file and its siblings, so a
	// headers.Get(someHeaderConst) read is held to the same rule as a literal.
	constants := packageStringConstants(t, filepath.Dir(thisFile))

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
		header, resolved := headerNameOf(call.Args[0], constants)
		if !resolved {
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

// headerNameOf resolves a headers.Get argument to the header name it reads: a
// string literal, or an identifier declared as a package-level string const.
func headerNameOf(arg ast.Expr, constants map[string]string) (string, bool) {
	switch node := arg.(type) {
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			return "", false
		}
		header, err := strconv.Unquote(node.Value)
		return header, err == nil
	case *ast.Ident:
		header, ok := constants[node.Name]
		return header, ok
	}
	return "", false
}

// packageStringConstants collects every package-level `const name = "value"` in
// the adapters package, so the scan above can follow one.
func packageStringConstants(t *testing.T, dir string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	require.NoError(t, err)

	constants := map[string]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				generic, ok := decl.(*ast.GenDecl)
				if !ok || generic.Tok != token.CONST {
					continue
				}
				for _, spec := range generic.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for index, name := range value.Names {
						if index >= len(value.Values) {
							continue
						}
						lit, ok := value.Values[index].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						if text, err := strconv.Unquote(lit.Value); err == nil {
							constants[name.Name] = text
						}
					}
				}
			}
		}
	}
	return constants
}
