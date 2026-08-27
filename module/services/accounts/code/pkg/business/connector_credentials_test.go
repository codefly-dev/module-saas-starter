package business_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/githubconnector"
)

func testAppCredential(t *testing.T) *githubconnector.AppCredential {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return &githubconnector.AppCredential{AppID: "123456", InstallationID: "789", PrivateKeyPEM: string(keyPEM)}
}

func TestConnectorCredential_StoreAndLoadRoundTrip(t *testing.T) {
	clearData(t)
	ctx := testCtx
	_, orgID := mustUserAndOrg(t, ctx, "conn-roundtrip@rls-test.com", "conn-roundtrip", "Acme Connector")
	sourceID := uuid.NewString()

	app := testAppCredential(t)
	cred := business.SourceCredential{GitHubApp: app, WebhookSecret: "whsec-roundtrip"}
	require.NoError(t, testService.PutGitHubSourceCredential(ctx, orgID, sourceID, cred))

	got, err := testService.GetGitHubSourceCredential(ctx, orgID, sourceID)
	require.NoError(t, err)
	require.Equal(t, cred, got)

	// The stored secret is an envelope reference, never plaintext.
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		record, err := testStore.GetConnectorCredential(ctx, sourceID)
		require.NoError(t, err)
		require.NotNil(t, record)
		require.Equal(t, business.ConnectorProviderGitHub, record.Provider)
		require.True(t, strings.HasPrefix(record.SecretEncrypted, "cfs1:vault-transit:"),
			"secret must be persisted as an envelope, got %q", record.SecretEncrypted)
		require.NotContains(t, record.SecretEncrypted, "whsec-roundtrip")
		require.NotContains(t, record.SecretEncrypted, app.PrivateKeyPEM)
		return nil
	}))
}

func TestConnectorCredential_UpsertReplacesSecret(t *testing.T) {
	clearData(t)
	ctx := testCtx
	_, orgID := mustUserAndOrg(t, ctx, "conn-upsert@rls-test.com", "conn-upsert", "Acme Upsert")
	sourceID := uuid.NewString()

	require.NoError(t, testService.PutGitHubSourceCredential(ctx, orgID, sourceID,
		business.SourceCredential{WebhookSecret: "whsec-first"}))
	require.NoError(t, testService.PutGitHubSourceCredential(ctx, orgID, sourceID,
		business.SourceCredential{WebhookSecret: "whsec-second"}))

	got, err := testService.GetGitHubSourceCredential(ctx, orgID, sourceID)
	require.NoError(t, err)
	require.Equal(t, "whsec-second", got.WebhookSecret)
}

func TestConnectorCredential_CrossTenantIsolation(t *testing.T) {
	clearData(t)
	ctx := testCtx
	_, orgA := mustUserAndOrg(t, ctx, "conn-a@rls-test.com", "conn-a", "Acme A")
	_, orgB := mustUserAndOrg(t, ctx, "conn-b@rls-test.com", "conn-b", "Acme B")
	sourceID := uuid.NewString()

	require.NoError(t, testService.PutGitHubSourceCredential(ctx, orgA, sourceID,
		business.SourceCredential{WebhookSecret: "whsec-a"}))

	// B cannot read A's source credential.
	_, err := testService.GetGitHubSourceCredential(ctx, orgB, sourceID)
	require.ErrorIs(t, err, business.ErrSourceCredentialNotFound)

	// A direct store read from B's transaction is hidden by RLS.
	require.NoError(t, testStore.WithOrgTx(ctx, orgB, func(ctx context.Context) error {
		record, err := testStore.GetConnectorCredential(ctx, sourceID)
		require.NoError(t, err)
		require.Nil(t, record, "RLS must hide A's credential from B")
		return nil
	}))
}

func TestConnectorCredential_SigningSecretIsPreAuthCrossTenant(t *testing.T) {
	clearData(t)
	ctx := testCtx
	_, orgID := mustUserAndOrg(t, ctx, "conn-sign@rls-test.com", "conn-sign", "Acme Sign")
	sourceID := uuid.NewString()

	require.NoError(t, testService.PutGitHubSourceCredential(ctx, orgID, sourceID,
		business.SourceCredential{WebhookSecret: "whsec-inbound"}))

	// The webhook receiver has no tenant session; the lookup resolves the secret
	// by source id alone through the control plane.
	secret, err := testService.SourceSigningSecret(context.Background(), sourceID)
	require.NoError(t, err)
	require.Equal(t, "whsec-inbound", secret)

	// An unknown source, and a source without a webhook secret, both fail closed.
	_, err = testService.SourceSigningSecret(context.Background(), uuid.NewString())
	require.ErrorIs(t, err, business.ErrSourceCredentialNotFound)

	// A malformed (non-UUID) source id — the shape an attacker probes the
	// unauthenticated webhook path with — is a clean miss, not a distinct error,
	// so the caller does not log every probe as a resolver failure.
	_, err = testService.SourceSigningSecret(context.Background(), "not-a-uuid")
	require.ErrorIs(t, err, business.ErrSourceCredentialNotFound)

	appOnlySource := uuid.NewString()
	require.NoError(t, testService.PutGitHubSourceCredential(ctx, orgID, appOnlySource,
		business.SourceCredential{GitHubApp: testAppCredential(t)}))
	_, err = testService.SourceSigningSecret(context.Background(), appOnlySource)
	require.ErrorIs(t, err, business.ErrSourceCredentialNotFound)
}

func TestConnectorCredential_Delete(t *testing.T) {
	clearData(t)
	ctx := testCtx
	_, orgID := mustUserAndOrg(t, ctx, "conn-del@rls-test.com", "conn-del", "Acme Delete")
	sourceID := uuid.NewString()

	require.NoError(t, testService.PutGitHubSourceCredential(ctx, orgID, sourceID,
		business.SourceCredential{WebhookSecret: "whsec-del"}))
	require.NoError(t, testService.DeleteSourceCredential(ctx, orgID, sourceID))

	_, err := testService.GetGitHubSourceCredential(ctx, orgID, sourceID)
	require.ErrorIs(t, err, business.ErrSourceCredentialNotFound)

	// Deleting an absent credential is a no-op.
	require.NoError(t, testService.DeleteSourceCredential(ctx, orgID, sourceID))
}

func TestConnectorCredential_Validation(t *testing.T) {
	clearData(t)
	ctx := testCtx
	_, orgID := mustUserAndOrg(t, ctx, "conn-valid@rls-test.com", "conn-valid", "Acme Valid")

	// A non-UUID source id is rejected before touching the store.
	err := testService.PutGitHubSourceCredential(ctx, orgID, "not-a-uuid",
		business.SourceCredential{WebhookSecret: "x"})
	require.ErrorContains(t, err, "uuid")

	// An empty credential set is rejected.
	err = testService.PutGitHubSourceCredential(ctx, orgID, uuid.NewString(), business.SourceCredential{})
	require.Error(t, err)
}

func TestConnectorCredential_FetchGitHubSourceContents(t *testing.T) {
	clearData(t)
	ctx := testCtx
	_, orgID := mustUserAndOrg(t, ctx, "conn-fetch@rls-test.com", "conn-fetch", "Acme Fetch")
	sourceID := uuid.NewString()

	want := []byte("# Wiki root\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_fetch",
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		case strings.Contains(r.URL.Path, "/contents/"):
			require.Equal(t, "Bearer ghs_fetch", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "file", "name": "README.md", "path": "README.md", "sha": "abc",
				"size": len(want), "encoding": "base64",
				"content": base64.StdEncoding.EncodeToString(want),
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	testService.SetGitHubConnector(githubconnector.NewConnector(githubconnector.WithBaseURL(server.URL)))
	t.Cleanup(func() { testService.SetGitHubConnector(githubconnector.NewConnector()) })

	require.NoError(t, testService.PutGitHubSourceCredential(ctx, orgID, sourceID,
		business.SourceCredential{GitHubApp: testAppCredential(t)}))

	content, err := testService.FetchGitHubSourceContents(ctx, orgID, sourceID, "acme", "wiki", "README.md", "main")
	require.NoError(t, err)
	require.Equal(t, githubconnector.ContentTypeFile, content.Type)
	require.Equal(t, want, content.Content)
}
