package adapters

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codefly-dev/core/wool"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
)

// A capability minted BEFORE the cutover — the pre-core JSON payload, signed by
// the same key — is refused by every read-back, and the operator is told why:
// the refusal to the caller stays "invalid", and the log names the encoding,
// never the token.
func TestR1093_PreCutoverJSONCapabilityIsRefusedAndLogged(t *testing.T) {
	logs := captureAdapterLogs(t)
	server := newCoreTestServer(t, &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 12}}, newSealAnswersFake(12))
	payload, err := json.Marshal(map[string]any{
		"typ": "codefly-work-context+v1", "alg": "EdDSA", "kid": server.keyID, "iss": server.issuer,
		"aud": "tool.test", "tenant_id": renewOrgID, "owner_principal_id": renewActorID,
		"exp": time.Now().Add(time.Minute).Unix(), "nonce": "n-1",
	})
	require.NoError(t, err)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(server.key, payload))

	_, err = server.readBack(context.Background(), token)
	require.ErrorIs(t, err, workcontext.ErrNotACoreToken)
	_, _, err = server.verifyParent(context.Background(), renewOrgID, renewActorID, token)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "the caller learns only that it is invalid")

	logged := logs()
	require.Contains(t, logged, "pre-cutover encoding")
	require.Contains(t, logged, "not a core token")
	require.NotContains(t, logged, token[:24], "the token is never logged")
}

// captureAdapterLogs redirects wool's fallback logger — the one Get() uses for a
// context carrying no provider — and returns a reader for what was logged.
func captureAdapterLogs(t *testing.T) func() string {
	t.Helper()
	capture := &adapterLogCapture{}
	wool.SetFallbackLogger(capture)
	t.Cleanup(func() { wool.SetFallbackLogger(nil) })
	return capture.String
}

type adapterLogCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *adapterLogCapture) Process(msg *wool.Log) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, msg.String())
}

func (c *adapterLogCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}
