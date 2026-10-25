//go:build !pure

package business_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/datasource/apisource"
	"accounts/pkg/datasource/connector/connectortest"
	"accounts/pkg/datasource/operations"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// #1087, at the service level: an httptest provider and API beside the host,
// reached through a client_credentials source with the real store, the real
// Vault envelope and the REAL guarded transport — neither the token exchange
// nor the operation client is substituted. The only thing that lets the dials
// through is a local runtime's allowance naming the stub's exact address.
func TestClientCredentialsSourceReachesALocalStubThroughTheEgressAllowance(t *testing.T) {
	clearData(t)
	actor, org := mustUserAndOrg(t, testCtx, "local-egress@example.com", "local-egress", "Acme")
	svc, _ := realOAuthService(t)
	clientSecret := randomCredential(t)
	const accessToken = "stub-access-token-0123456789abcdef"
	var tokenCalls, apiCalls atomic.Int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenCalls.Add(1)
			if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "client_credentials" ||
				r.PostForm.Get("client_id") != "example-client" || r.PostForm.Get("client_secret") != clientSecret {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": accessToken, "expires_in": 3600})
		case "/items/a":
			apiCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer "+accessToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer stub.Close()
	t.Cleanup(func() { _ = apisource.ConfigureLocalEgress("", false) })

	config := &business.APIDatasourceConfig{
		BaseURL:        stub.URL,
		CredentialKind: business.APICredentialKindOAuth2,
		OAuth2: &business.APIOAuth2Config{
			Grant:    business.OAuth2ClientCredentials,
			TokenURL: stub.URL + "/token",
			ClientID: "example-client",
		},
	}
	source, err := svc.AddSource(testCtx, actor, business.AddSourceInput{OrgID: org, Provider: business.DatasourceProviderAPI, CollectionLabel: "Acme", API: config, Credential: clientSecret})
	require.NoError(t, err)

	// No allowance: the public-address guard refuses the token exchange, and
	// the stub never sees a request.
	require.NoError(t, apisource.ConfigureLocalEgress("", true))
	copy := *source
	_, err = svc.ResolveDatasourceTokenForTest(testCtx, &copy)
	require.Error(t, err, "loopback must stay unreachable without an allowance")
	require.Zero(t, tokenCalls.Load())

	require.NoError(t, apisource.ConfigureLocalEgress(stub.Listener.Addr().String(), true))
	declaration := connectortest.OperationDeclaration()
	requestCtx := auth.WithVerifiedDatabaseIdentity(testCtx, actor, org)
	grantOperationBoundary(t, requestCtx, actor, org, source.BoundaryNodeID)
	_, err = svc.DeclareSourceOperations(requestCtx, actor, org, source.ID, []operations.Declaration{declaration})
	require.NoError(t, err)
	receiptStore, closeStore, err := testStore.NewEffectReceipts()
	require.NoError(t, err)
	defer closeStore()
	require.NoError(t, svc.ConfigureSourceOperationReceipts(receiptStore))
	_, guard := svc.SourceOperationReceipts()
	const effect, input = "local-egress", `{"id":"a"}`
	request := &gen.InvokeSourceOperationRequest{OrgId: org, SourceId: source.ID, Operation: declaration.Name, InputJson: input, EffectId: effect}
	ctx := business.WithSourceOperationRecheck(requestCtx, func(ctx context.Context) error {
		_, _, err := svc.SourceOperationAuthority(ctx, actor, org, source.ID, declaration.Name, effect, "", false)
		return err
	})
	response, err := guard.Handle(ctx, business.SourceOperationMethod, effect, request, func(ctx context.Context) (proto.Message, error) {
		var response *gen.InvokeSourceOperationResponse
		_, err := svc.InvokeSourceOperation(ctx, actor, org, source.ID, declaration.Name, "", []byte(input), func(ctx context.Context, result *business.SourceOperationResult) error {
			response = &gen.InvokeSourceOperationResponse{OutputJson: string(result.Output), Receipt: &gen.SourceOperationReceipt{EffectId: effect, CommittedAt: result.CommittedAt.UTC().Format(time.RFC3339Nano), Status: "committed", ProviderStatus: uint32(result.ProviderStatus), OutputJson: string(result.Output)}}
			return svc.RecordSourceOperationReceipt(ctx, response)
		})
		return response, err
	})
	require.NoError(t, err, "the listed stub must be reachable for both the token exchange and the operation call")
	require.Equal(t, "{}", response.(*gen.InvokeSourceOperationResponse).OutputJson)
	require.EqualValues(t, 1, tokenCalls.Load(), "one client_credentials exchange")
	require.EqualValues(t, 1, apiCalls.Load(), "one operation call, with the exchanged token")

	// Withdrawn, the same source cannot reach the stub again: a fresh exchange
	// on a second source is refused before it is sent.
	require.NoError(t, apisource.ConfigureLocalEgress("", true))
	second, err := svc.AddSource(testCtx, actor, business.AddSourceInput{OrgID: org, Provider: business.DatasourceProviderAPI, CollectionLabel: "Acme", API: config, Credential: clientSecret})
	require.NoError(t, err)
	copy = *second
	_, err = svc.ResolveDatasourceTokenForTest(testCtx, &copy)
	require.Error(t, err)
	require.EqualValues(t, 1, tokenCalls.Load(), "a withdrawn allowance must not reach the stub")
}
