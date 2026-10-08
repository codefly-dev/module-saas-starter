package main

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/workcontext"
	"github.com/stretchr/testify/require"
)

// A target-scoped, cryptographically valid Work Context is currently an
// attenuation, not a substitute for a gateway access credential. This pins the
// existing perimeter while a separate installed headless transport is designed.
func TestGateway_Module_ValidWorkContextAloneDoesNotAuthenticate(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	upstream, address := newModuleUpstream(t)
	require.Equal(t, http.StatusOK, registerModule(t, gw, priv, "documents", address).Code)
	keys := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{"headless-test": priv.Public().(ed25519.PublicKey)}), nil)
	gw.workContext = newWorkContextVerifier(keys.URL)
	signer, err := workcontext.NewWorkContextSigner(workcontext.WorkContextSignerOptions{
		Issuer: "saas-starter", KeyID: "headless-test", PrivateKey: priv,
	})
	require.NoError(t, err)
	token, _, err := signer.StartTask(workcontext.StartTaskInput{
		Audience: "documents", TenantID: "tenant-1", OwnerPrincipalID: "user-1",
		TaskID: "task-1", SessionID: "session-1",
		AuthorityScopes: []*basev0.WorkScopeV1{{ResourceKind: "documents", Actions: []string{"read"}}},
	})
	require.NoError(t, err)
	require.NoError(t, gw.workContext.Verify(context.Background(), token), "must not mistake invalid capability rejection for bearer enforcement")
	req := httptest.NewRequest(http.MethodGet, "/v1/documents/collection", nil)
	req.Header.Set(workcontext.WorkContextHeaderName, token.Encoded())
	result := httptest.NewRecorder()
	gw.ServeHTTP(result, req)
	require.Equal(t, http.StatusUnauthorized, result.Code)
	require.Contains(t, result.Body.String(), "authentication required")
	require.Nil(t, upstream.lastHeaders, "no effect may reach the registered target")
}
