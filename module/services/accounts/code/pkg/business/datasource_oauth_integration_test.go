//go:build !pure

package business_test

import (
	"accounts/pkg/auth"
	"accounts/pkg/datasource/connector/connectortest"
	"accounts/pkg/datasource/operations"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codefly-dev/core/wool"
	"github.com/codefly-dev/sdk-go/receipts"

	"accounts/pkg/business"
	"accounts/pkg/datasource/apisource"
	"accounts/pkg/keyservice"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func realOAuthService(t *testing.T) (*business.Service, business.SecretCipher) {
	t.Helper()
	keys, err := keyservice.Load(testCtx)
	require.NoError(t, err, "real Vault is required")
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	svc.SetDatasourceConnector(keys.Cipher, nil, "")
	seed := make([]byte, 32)
	_, err = rand.Read(seed)
	require.NoError(t, err)
	svc.SetDatasourceTicketKey(seed)
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	t.Cleanup(emitter.Close)
	svc.SetAuditEmitter(emitter)
	return svc, keys.Cipher
}
func randomCredential(t *testing.T) string {
	t.Helper()
	b := make([]byte, 48)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(b)
}

func TestClientCredentialsRealVaultAndConcurrentRowLock(t *testing.T) {
	clearData(t)
	actor, org := mustUserAndOrg(t, testCtx, "oauth-service@example.com", "oauth-service", "Acme")
	svc, cipher := realOAuthService(t)
	credential := randomCredential(t)
	cfg := oauthConfig()
	cfg.OAuth2.Grant = business.OAuth2ClientCredentials
	source, err := svc.AddSource(testCtx, actor, business.AddSourceInput{OrgID: org, Provider: business.DatasourceProviderAPI, CollectionLabel: "Acme", API: cfg, Credential: credential})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(source.CredentialSecretRef, "cfs1:"), "must be a real key-service envelope")
	require.NotContains(t, source.CredentialSecretRef, credential)
	var calls atomic.Int32
	svc.SetDatasourceClientCredentialsForTest(func(ctx context.Context, cfg apisource.OAuth2Config, clientID, secret string) (*apisource.OAuth2Token, error) {
		calls.Add(1)
		require.Equal(t, cfg.ClientID, clientID)
		require.Equal(t, credential, secret)
		return &apisource.OAuth2Token{AccessToken: "example-access-token", ExpiresIn: time.Hour}, nil
	})
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Go(func() {
			copy := *source
			token, err := svc.ResolveDatasourceTokenForTest(testCtx, &copy)
			if err == nil && token != "example-access-token" {
				t.Error("wrong token")
			}
			failures <- err
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, calls.Load(), "row lock must collapse concurrent exchanges")
	stored, err := svc.GetDatasourceSource(testCtx, org, source.ID)
	require.NoError(t, err)
	plain, err := cipher.DecryptSecret(testCtx, business.DatasourceConnectorSecretPurpose(source.ID), stored.CredentialSecretRef)
	require.NoError(t, err)
	var tokenSet map[string]any
	require.NoError(t, json.Unmarshal([]byte(plain), &tokenSet))
	require.Equal(t, "", tokenSet["refresh_token"])
	require.Equal(t, credential, tokenSet["client_secret"])
	require.Equal(t, "example-access-token", tokenSet["access_token"])
}

func TestAuthorizationCodeRealVaultPersonalSourceAndReplay(t *testing.T) {
	clearData(t)
	actor, org := mustUserAndOrg(t, testCtx, "oauth-person@example.com", "oauth-person", "Acme")
	svc, cipher := realOAuthService(t)
	clientSecret := randomCredential(t)
	refresh := randomCredential(t)
	secrets, _ := json.Marshal(map[string]string{"api": clientSecret})
	require.NoError(t, svc.ConfigureDatasourceOAuth(`{"api":{"authorize_url":"https://auth.example.com/authorize","token_url":"https://auth.example.com/token","scopes":["read","offline_access"]}}`, `{"api":"example-client"}`, string(secrets)))
	source, err := svc.AddSource(testCtx, actor, business.AddSourceInput{OrgID: org, Provider: business.DatasourceProviderAPI, CollectionLabel: "Acme", API: &business.APIDatasourceConfig{BaseURL: "https://api.example.com", CredentialKind: business.APICredentialKindOAuth2, OAuth2: &business.APIOAuth2Config{Grant: business.OAuth2AuthorizationCode}}})
	require.NoError(t, err)
	require.Equal(t, actor, source.PersonalOwnerUserID)
	require.Empty(t, source.CredentialSecretRef)
	handle, err := svc.BeginDatasourceAccountLink(testCtx, actor, org, "api", "https://host.example.com/callback", source.ID)
	require.NoError(t, err)
	authorize, err := url.Parse(handle.AuthorizeURL)
	require.NoError(t, err)
	calls := 0
	svc.SetDatasourceAuthorizationCodeForTest(func(ctx context.Context, cfg apisource.OAuth2Config, secret, code, verifier, redirect string) (*apisource.OAuth2Token, error) {
		calls++
		require.Equal(t, clientSecret, secret)
		require.Equal(t, "example-code", code)
		require.Equal(t, "https://host.example.com/callback", redirect)
		challenge := sha256.Sum256([]byte(verifier))
		require.Equal(t, authorize.Query().Get("code_challenge"), base64.RawURLEncoding.EncodeToString(challenge[:]))
		require.NotContains(t, handle.State, verifier)
		return &apisource.OAuth2Token{AccessToken: "example-access", RefreshToken: refresh, ExpiresIn: time.Second}, nil
	})
	_, err = svc.CompleteDatasourceAccountLink(testCtx, business.NewIDString(), org, handle.State, "example-code")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Zero(t, calls)
	_, err = svc.CompleteDatasourceAccountLink(testCtx, actor, org, handle.State, "example-code")
	require.NoError(t, err)
	_, err = svc.CompleteDatasourceAccountLink(testCtx, actor, org, handle.State, "example-code")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Equal(t, 1, calls)
	stored, err := svc.GetDatasourceSource(testCtx, org, source.ID)
	require.NoError(t, err)
	require.Equal(t, actor, stored.PersonalOwnerUserID)
	plain, err := cipher.DecryptSecret(testCtx, business.DatasourceConnectorSecretPurpose(source.ID), stored.CredentialSecretRef)
	require.NoError(t, err)
	var tokenSet map[string]any
	require.NoError(t, json.Unmarshal([]byte(plain), &tokenSet))
	require.Equal(t, refresh, tokenSet["refresh_token"])
	require.NotContains(t, tokenSet, "client_secret")
	require.Empty(t, stored.API.OAuth2.ClientID)
	require.Empty(t, stored.API.OAuth2.TokenURL)
	rotatedSecret := randomCredential(t)
	rotatedSecrets, _ := json.Marshal(map[string]string{"api": rotatedSecret})
	require.NoError(t, svc.ConfigureDatasourceOAuth(`{"api":{"authorize_url":"https://auth.example.com/authorize","token_url":"https://auth.example.com/rotated-token","scopes":["read"]}}`, `{"api":"rotated-client"}`, string(rotatedSecrets)))
	svc.SetDatasourceOAuth2RefreshFunc(func(ctx context.Context, cfg apisource.OAuth2Config, token, secret string) (*apisource.OAuth2Token, error) {
		require.Equal(t, refresh, token)
		require.Equal(t, rotatedSecret, secret)
		require.Equal(t, "rotated-client", cfg.ClientID)
		require.Equal(t, "https://auth.example.com/rotated-token", cfg.TokenURL)
		return &apisource.OAuth2Token{AccessToken: "rotated-access", RefreshToken: "rotated-refresh", ExpiresIn: time.Hour}, nil
	})
	access, err := svc.ResolveDatasourceTokenForTest(testCtx, stored)
	require.NoError(t, err)
	require.Equal(t, "rotated-access", access)
	plain, err = cipher.DecryptSecret(testCtx, business.DatasourceConnectorSecretPurpose(source.ID), stored.CredentialSecretRef)
	require.NoError(t, err)
	tokenSet = map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(plain), &tokenSet))
	require.NotContains(t, tokenSet, "client_secret")
	require.Equal(t, "rotated-refresh", tokenSet["refresh_token"])
	_, err = svc.ListSourceOperations(testCtx, business.NewIDString(), org, source.ID)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// This is the durable half of the transport disclosure guard. The real Vault
// envelope and Postgres audit sink stay in use; only outbound provider failures
// are scripted. The Connect receipt serialization is qualified with the public
// binding, separately from this business response check.
func TestNoCredentialByteSequenceLeaks(t *testing.T) {
	clearData(t)
	actor, org := mustUserAndOrg(t, testCtx, "disclosure@example.com", "disclosure", "Acme")
	svc, _ := realOAuthService(t)
	requestCtx := auth.WithVerifiedDatabaseIdentity(testCtx, actor, org)
	secret, refresh := randomCredential(t), randomCredential(t)
	captured := &woolCapture{}
	wool.SetFallbackLogger(captured)
	t.Cleanup(func() { wool.SetFallbackLogger(nil) })
	for index, mode := range []string{"bad_token_url", "401", "429", "redirect", "timeout", "schema"} {
		t.Run(mode, func(t *testing.T) {
			// Independent credential budgets ensure a 429 cannot mask later failures.
			cfg := oauthConfig()
			cfg.BaseURL = "https://" + strings.ReplaceAll(mode, "_", "-") + ".example.test"
			source, err := svc.AddSource(testCtx, actor, business.AddSourceInput{OrgID: org, Provider: business.DatasourceProviderAPI, CollectionLabel: "Acme", API: cfg, Credential: refresh, OAuth2ClientSecret: secret})
			require.NoError(t, err)
			grantOperationBoundary(t, requestCtx, actor, org, source.BoundaryNodeID)
			declarations, err := svc.DeclareSourceOperations(requestCtx, actor, org, source.ID, []operations.Declaration{connectortest.OperationDeclaration()})
			require.NoError(t, err)
			require.Len(t, declarations, 1)
			svc.SetDatasourceOAuth2RefreshFunc(func(ctx context.Context, cfg apisource.OAuth2Config, token, clientSecret string) (*apisource.OAuth2Token, error) {
				if mode == "bad_token_url" {
					cfg.TokenURL = "://" + secret
					return apisource.RefreshOAuth2(ctx, cfg, token, clientSecret)
				}
				return &apisource.OAuth2Token{AccessToken: secret, ExpiresIn: time.Hour}, nil
			})
			var upstream error
			switch mode {
			case "401":
				upstream = &apisource.Failure{Reason: secret, ProviderStatus: 401}
			case "429":
				upstream = &apisource.Failure{Reason: secret, ProviderStatus: 429, RetryAfter: time.Minute}
			default:
				upstream = errors.New(mode + ":" + secret + ":" + refresh)
			}
			svc.SetSourceOperationClientForTest(func(apisource.Config, string) business.APIOperationClient { return operationFailureClient{upstream} })
			input := json.RawMessage(`{"id":"a"}`)
			if mode == "schema" {
				input, _ = json.Marshal(map[string]string{"id": "a", "unexpected": secret})
			}
			effectID := business.NewIDString()
			sum := sha256.Sum256(input)
			ctx := receipts.WithEffect(requestCtx, receipts.Effect{ID: effectID, Tenant: org, Method: "/saas.accounts.v1.DatasourceService/InvokeSourceOperation", RequestDigest: sum[:]})
			response, err := svc.InvokeSourceOperation(ctx, actor, org, source.ID, "read_item", input, func(context.Context, *business.SourceOperationResult) error {
				t.Fatal("failure must not commit a receipt")
				return nil
			})
			require.Error(t, err)
			want := map[string]codes.Code{"bad_token_url": codes.Unavailable, "401": codes.FailedPrecondition,
				"429": codes.ResourceExhausted, "redirect": codes.Unavailable, "timeout": codes.Unavailable, "schema": codes.InvalidArgument}
			require.Equal(t, want[mode], status.Code(err), "failure must reach the intended layer")
			encoded, marshalErr := json.Marshal(response)
			require.NoError(t, marshalErr)
			var rows []business.AuditEntry
			require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
				var err error
				rows, _, _, err = testStore.QueryAuditLog(ctx, business.AuditQuery{OrgID: org, EventType: string(business.EventDatasourceOperationInvoked), PageSize: 100})
				return err
			}))
			require.Len(t, rows, index+1, "exactly one audit row per attempt")
			auditJSON, marshalErr := json.Marshal(rows)
			require.NoError(t, marshalErr)
			connectortest.AssertNoCredentialSegments(t, []string{secret, refresh}, err.Error(), string(encoded), string(auditJSON), captured.String())
		})
	}
}
