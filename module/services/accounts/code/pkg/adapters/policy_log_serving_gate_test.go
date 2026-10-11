package adapters

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

// The gate is consulted, and it is consulted on every call both transports
// serve.
//
// Two halves, because the regression this file exists to catch has two shapes.
// One is a gate that is registered and then never asked — which is the state
// this whole protocol was in before: built, tested, and on no code path. The
// other is a gate asked on the unary path only, so a streaming call serves
// straight through a host that must not be serving.

func TestARegisteredGateRefusesTheCall(t *testing.T) {
	t.Cleanup(func() { RegisterPolicyLogServingGate(nil) })

	refusal := errors.New("not serving")
	var asked int
	RegisterPolicyLogServingGate(func(context.Context) error {
		asked++
		return refusal
	})

	require.ErrorIs(t, enforcePolicyLogServing(context.Background()), refusal)
	require.Equal(t, 1, asked, "the gate must be asked, not assumed")
}

func TestNoRegisteredGateAdmits(t *testing.T) {
	t.Cleanup(func() { RegisterPolicyLogServingGate(nil) })
	RegisterPolicyLogServingGate(nil)

	// A deployment with no policy log has narrowed nothing through the
	// protocol, so it has nothing unreconciled to honour. The refusal such a
	// host owes is on the narrowing path, not here.
	require.NoError(t, enforcePolicyLogServing(context.Background()))
}

// Every authorization path of both transports asks the gate.
//
// A source check rather than four transport round trips, because what can
// regress is a missing CALL: the interceptors are four nearly identical
// sequences of enforce* steps, and a step dropped from one of them is invisible
// to any test that drives the other three. The four functions are named
// explicitly so that a NEW interceptor path does not silently inherit a pass.
func TestBothTransportsAskTheGateOnEveryPath(t *testing.T) {
	want := map[string]string{
		"grpc_auth_interceptor.go:grpcAuthInterceptor":       "unary gRPC",
		"grpc_auth_interceptor.go:grpcStreamAuthInterceptor": "streaming gRPC",
		"connect_auth_interceptor.go:WrapUnary":              "unary Connect",
		"connect_auth_interceptor.go:WrapStreamingHandler":   "streaming Connect",
	}
	found := map[string]bool{}

	for _, file := range []string{"grpc_auth_interceptor.go", "connect_auth_interceptor.go"} {
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, file, nil, 0)
		require.NoError(t, err)
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			key := file + ":" + fn.Name.Name
			ast.Inspect(fn, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "enforcePolicyLogServing" {
					found[key] = true
				}
				return true
			})
		}
	}

	for key, what := range want {
		require.True(t, found[key],
			"%s (%s) does not consult the policy log serving gate, so that path serves a host that must not be serving",
			key, what)
	}
}
