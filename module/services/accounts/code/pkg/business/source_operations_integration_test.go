//go:build !pure

package business_test

import (
	"accounts/pkg/auth"
	"accounts/pkg/datasource/apisource"
	"accounts/pkg/datasource/connector/connectortest"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"encoding/json"
	"github.com/codefly-dev/sdk-go/receipts"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/datasource/operations"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSourceDeclarationsAtomicReplacementAndTenantIsolation(t *testing.T) {
	clearData(t)
	actor, org := mustUserAndOrg(t, testCtx, "source-owner@example.com", "source-owner", "Acme")
	_, other := mustUserAndOrg(t, testCtx, "source-other@example.com", "source-other", "ExampleCorp")
	svc, _ := realOAuthService(t)
	source, err := svc.AddSource(testCtx, actor, business.AddSourceInput{OrgID: org, Provider: business.DatasourceProviderAPI, CollectionLabel: "Acme", Credential: randomCredential(t), API: apiConfig()})
	require.NoError(t, err)
	decl := operations.Declaration{Name: "read_item", Method: "GET", Path: "/items/{id}", Effect: operations.ReadOnly, MaxOutputBytes: 1024,
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"id":{"type":"string"}},"required":["id"]}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}
	admitted, err := svc.DeclareSourceOperations(testCtx, actor, org, source.ID, []operations.Declaration{decl})
	require.NoError(t, err)
	require.Len(t, admitted, 1)
	require.NotEmpty(t, admitted[0].Digest)
	_, err = svc.DeclareSourceOperations(testCtx, actor, org, source.ID, []operations.Declaration{decl, decl})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		got, err := testStore.ListSourceOperations(ctx, org, source.ID)
		require.NoError(t, err)
		require.Equal(t, admitted, got)
		return nil
	}))
	require.NoError(t, testStore.WithOrgTx(testCtx, other, func(ctx context.Context) error {
		got, err := testStore.ListSourceOperations(ctx, org, source.ID)
		require.NoError(t, err)
		require.Empty(t, got, "RLS must hide the other tenant's declarations")
		return nil
	}))
	_, err = svc.DeclareSourceOperations(testCtx, business.NewIDString(), org, source.ID, nil)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = svc.DeclareSourceOperations(testCtx, actor, org, source.ID, nil)
	require.NoError(t, err)
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		got, err := testStore.ListSourceOperations(ctx, org, source.ID)
		require.Empty(t, got)
		return err
	}))
}

// This qualification uses the real tenant pool and Vault envelope, with the
// SDK receipt guard and transaction writer. Only outbound provider transport is
// replaced to reach httptest; resolved-IP egress is covered in apisource.
func TestSourceOperationRealReceiptReplayAndUnknownMutation(t *testing.T) {
	clearData(t)
	actor, org := mustUserAndOrg(t, testCtx, "operation-owner@example.com", "operation-owner", "Acme")
	svc, _ := realOAuthService(t)
	secret := randomCredential(t)
	var calls atomic.Int64
	var lose atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "Bearer "+secret, r.Header.Get("Authorization"))
		if lose.Load() {
			connection, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			_ = connection.Close()
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer provider.Close()
	config := apiConfig()
	config.BaseURL = provider.URL
	source, err := svc.AddSource(testCtx, actor, business.AddSourceInput{OrgID: org, Provider: business.DatasourceProviderAPI, CollectionLabel: "Acme", Credential: secret, API: config})
	require.NoError(t, err)
	declaration := connectortest.OperationDeclaration()
	declaration.Effect = operations.Mutation
	_, err = svc.DeclareSourceOperations(testCtx, actor, org, source.ID, []operations.Declaration{declaration})
	require.NoError(t, err)
	receiptStore, closeStore, err := testStore.NewEffectReceipts()
	require.NoError(t, err)
	defer closeStore()
	require.NoError(t, svc.ConfigureSourceOperationReceipts(receiptStore))
	svc.SetSourceOperationClientForTest(func(_ apisource.Config, credential string) business.APIOperationClient {
		return operationHTTPClient{credential: credential}
	})
	_, guard := svc.SourceOperationReceipts()
	invoke := func(effect, input string) (proto.Message, error) {
		request := &gen.InvokeSourceOperationRequest{OrgId: org, SourceId: source.ID, Operation: declaration.Name, InputJson: input, EffectId: effect}
		ctx := auth.WithVerifiedDatabaseIdentity(testCtx, actor, org)
		ctx = business.WithSourceOperationRecheck(ctx, func(ctx context.Context) error {
			_, _, err := svc.SourceOperationAuthority(ctx, actor, org, source.ID, declaration.Name, effect, false)
			return err
		})
		return guard.Handle(ctx, business.SourceOperationMethod, effect, request, func(ctx context.Context) (proto.Message, error) {
			var response *gen.InvokeSourceOperationResponse
			_, err := svc.InvokeSourceOperation(ctx, actor, org, source.ID, declaration.Name, []byte(input), func(ctx context.Context, result *business.SourceOperationResult) error {
				response = &gen.InvokeSourceOperationResponse{OutputJson: string(result.Output), Receipt: &gen.SourceOperationReceipt{EffectId: effect, CommittedAt: result.CommittedAt.UTC().Format(time.RFC3339Nano), Status: "committed", ProviderStatus: uint32(result.ProviderStatus), OutputJson: string(result.Output)}}
				return svc.RecordSourceOperationReceipt(ctx, response)
			})
			return response, err
		})
	}
	first, err := invoke("committed", `{"id":"a"}`)
	require.NoError(t, err)
	replay, err := invoke("committed", `{"id":"a"}`)
	require.NoError(t, err)
	require.True(t, proto.Equal(first, replay))
	require.EqualValues(t, 1, calls.Load())
	_, err = invoke("committed", `{"id":"b"}`)
	require.ErrorIs(t, err, receipts.ErrEffectIDReused)
	require.EqualValues(t, 1, calls.Load())
	saved, found, err := receiptStore.Lookup(testCtx, org, "committed", business.SourceOperationMethod)
	require.NoError(t, err)
	require.True(t, found)
	wire := &gen.InvokeSourceOperationResponse{}
	require.NoError(t, proto.Unmarshal(saved.Response, wire))
	require.True(t, proto.Equal(first, wire))
	encoded, err := protojson.Marshal(wire)
	require.NoError(t, err)
	connectortest.AssertNoCredentialSegments(t, []string{secret}, string(encoded))
	_, found, err = receiptStore.Lookup(testCtx, business.NewIDString(), "committed", business.SourceOperationMethod)
	require.NoError(t, err)
	require.False(t, found)
	lose.Store(true)
	for range 2 {
		_, err = invoke("unresolved", `{"id":"a"}`)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.Contains(t, err.Error(), "outcome is unknown")
	}
	require.EqualValues(t, 2, calls.Load(), "an unknown mutation must never be sent again")
	_, found, err = receiptStore.Lookup(testCtx, org, "unresolved", business.SourceOperationMethod)
	require.NoError(t, err)
	require.False(t, found)
	attempt, err := svc.SourceOperationAttemptForActor(testCtx, actor, org, "unresolved")
	require.NoError(t, err)
	require.NotNil(t, attempt)
}
