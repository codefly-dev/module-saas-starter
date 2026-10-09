//go:build !pure

package business_test

import (
	"accounts/pkg/auth"
	"accounts/pkg/datasource/apisource"
	"accounts/pkg/datasource/connector/connectortest"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codefly-dev/sdk-go/receipts"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

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
	requestCtx := auth.WithVerifiedDatabaseIdentity(testCtx, actor, org)
	admitted, err := svc.DeclareSourceOperations(requestCtx, actor, org, source.ID, []operations.Declaration{decl})
	require.NoError(t, err)
	require.Len(t, admitted, 1)
	require.NotEmpty(t, admitted[0].Digest)
	_, err = svc.DeclareSourceOperations(requestCtx, actor, org, source.ID, []operations.Declaration{decl, decl})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		got, err := testStore.ListSourceOperations(ctx, org, source.ID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		canonical, err := operations.Admit(got[0])
		require.NoError(t, err)
		require.Equal(t, admitted[0], canonical)
		return nil
	}))
	require.NoError(t, testStore.WithOrgTx(testCtx, other, func(ctx context.Context) error {
		got, err := testStore.ListSourceOperations(ctx, org, source.ID)
		require.NoError(t, err)
		require.Empty(t, got, "RLS must hide the other tenant's declarations")
		return nil
	}))
	absent := business.NewIDString()
	_, err = svc.DeclareSourceOperations(auth.WithVerifiedDatabaseIdentity(testCtx, absent, org), absent, org, source.ID, nil)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = svc.DeclareSourceOperations(requestCtx, actor, org, source.ID, nil)
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
	requestCtx := auth.WithVerifiedDatabaseIdentity(testCtx, actor, org)
	grant := grantOperationBoundary(t, requestCtx, actor, org, source.BoundaryNodeID)
	_, err = svc.DeclareSourceOperations(requestCtx, actor, org, source.ID, []operations.Declaration{declaration})
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
	require.NoError(t, testService.RevokeScope(requestCtx, actor, &gen.RevokeScopeRequest{
		OrgId: org, SubjectId: actor, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		ScopePath: grant.ScopePath, RoleId: grant.RoleId,
	}))
	_, err = invoke("committed", `{"id":"a"}`)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "revoked authority must refuse a committed replay")
	require.EqualValues(t, 2, calls.Load())
}

// Grant exactly this boundary using the same role/scope rows that production
// permission checks read. Organization ownership alone is not a scope grant.
func grantOperationBoundary(t *testing.T, ctx context.Context, actor, org, node string) *gen.ScopeGrant {
	t.Helper()
	var grant *gen.ScopeGrant
	require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
		collections, err := testStore.ListCollectionAccess(ctx, org, "", 100, []string{"datasource"})
		if err != nil {
			return err
		}
		var path string
		for _, collection := range collections {
			if collection.GetNode().GetId() == node {
				path = collection.GetNode().GetScopePath()
			}
		}
		require.NotEmpty(t, path)
		role := business.NewIDString()
		if err := testStore.CreateRole(ctx, &gen.Role{Id: role, Name: "operation " + role, OrgId: org,
			Permissions: []*gen.Permission{{Resource: "datasource", Action: "invoke"}, {Resource: "datasource", Action: "read"}}}); err != nil {
			return err
		}
		grant = &gen.ScopeGrant{Id: business.NewIDString(), OrgId: org, SubjectId: actor,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: path, RoleId: role, GrantedBy: actor}
		return testStore.GrantScope(ctx, grant)
	}))
	return grant
}

// Backdate only the storage clock; transactions, RLS and SDK serialization stay
// real. This avoids either sleeping through retention or bypassing DB grants.
type sourceReceiptClock struct {
	receipts.Store
	before time.Time
}

func (s *sourceReceiptClock) Record(ctx context.Context, tx receipts.Tx, receipt receipts.Receipt) error {
	if !s.before.IsZero() {
		receipt.CommittedAt = s.before
	}
	return s.Store.Record(ctx, tx, receipt)
}

func TestSourceOperationRealReceiptRetentionAndTombstones(t *testing.T) {
	clearData(t)
	actor, org := mustUserAndOrg(t, testCtx, "retention@example.com", "retention", "Acme")
	otherActor, otherOrg := mustUserAndOrg(t, testCtx, "other-retention@example.com", "other-retention", "ExampleCorp")
	svc, _ := realOAuthService(t)
	sourceFor := func(actor, org string) *business.DatasourceSource {
		source, err := svc.AddSource(testCtx, actor, business.AddSourceInput{OrgID: org, Provider: business.DatasourceProviderAPI, CollectionLabel: "Example", Credential: randomCredential(t), API: apiConfig()})
		require.NoError(t, err)
		return source
	}
	source, otherSource, foreign := sourceFor(actor, org), sourceFor(actor, org), sourceFor(otherActor, otherOrg)
	requestCtx := auth.WithVerifiedDatabaseIdentity(testCtx, actor, org)
	grantOperationBoundary(t, requestCtx, actor, org, source.BoundaryNodeID)
	d := connectortest.OperationDeclaration()
	d.Effect = operations.Mutation
	declared, err := svc.DeclareSourceOperations(requestCtx, actor, org, source.ID, []operations.Declaration{d})
	require.NoError(t, err)
	receiptStore, closeStore, err := testStore.NewEffectReceipts()
	require.NoError(t, err)
	defer closeStore()
	clock := &sourceReceiptClock{Store: receiptStore}
	require.NoError(t, svc.ConfigureSourceOperationReceipts(clock))
	seed := func(src *business.DatasourceSource, owner, effect string, age time.Duration, committed bool) {
		digest := []byte("fixture request digest")
		ctx := auth.WithVerifiedDatabaseIdentity(testCtx, owner, src.OrgID)
		ctx = receipts.WithEffect(ctx, receipts.Effect{ID: effect, Tenant: src.OrgID, Method: business.SourceOperationMethod, RequestDigest: digest})
		clock.before = time.Now().Add(-age)
		require.NoError(t, testStore.WithOrgTx(ctx, src.OrgID, func(ctx context.Context) error {
			created, err := testStore.CreateSourceOperationAttempt(ctx, business.SourceOperationAttempt{OrgID: src.OrgID, ActorID: owner, SourceID: src.ID, Operation: d.Name, DeclarationDigest: declared[0].Digest, EffectID: effect, RequestDigest: digest})
			require.NoError(t, err)
			require.True(t, created)
			if !committed {
				return nil
			}
			return svc.RecordSourceOperationReceipt(ctx, &gen.InvokeSourceOperationResponse{OutputJson: `{}`, Receipt: &gen.SourceOperationReceipt{EffectId: effect, Status: "committed", OutputJson: `{}`}})
		}))
	}
	seed(source, actor, "expired", 48*time.Hour, true)
	seed(source, actor, "recent", 0, true)
	seed(otherSource, actor, "other-source", 48*time.Hour, true)
	seed(foreign, otherActor, "foreign", 48*time.Hour, true)
	seed(source, actor, "uncertain", 48*time.Hour, false)
	clock.before = time.Time{}
	_, guard := svc.SourceOperationReceipts()
	request := &gen.PruneSourceOperationReceiptsRequest{OrgId: org, SourceId: source.ID, EffectId: "cleanup"}
	invoke := func() (proto.Message, error) {
		ctx := business.WithSourceOperationRecheck(requestCtx, func(ctx context.Context) error {
			return svc.SourceReceiptRetentionAuthority(ctx, actor, org, source.ID, "cleanup", false)
		})
		return guard.Handle(ctx, business.SourceReceiptRetentionMethod, "cleanup", request, func(ctx context.Context) (proto.Message, error) {
			var response *gen.PruneSourceOperationReceiptsResponse
			err := svc.PruneSourceOperationReceipts(ctx, actor, org, source.ID, func(ctx context.Context, removed int64) error {
				response = &gen.PruneSourceOperationReceiptsResponse{EffectId: "cleanup", Receipts: removed, Status: "committed"}
				return svc.RecordSourceOperationReceipt(ctx, response)
			})
			return response, err
		})
	}
	failedCtx := receipts.WithEffect(requestCtx, receipts.Effect{ID: "cleanup-failed", Tenant: org, Method: business.SourceReceiptRetentionMethod, RequestDigest: []byte("cleanup")})
	commitAttempted := false
	err = svc.PruneSourceOperationReceipts(failedCtx, actor, org, source.ID, func(context.Context, int64) error { commitAttempted = true; return errors.New("receipt unavailable") })
	require.True(t, commitAttempted, "cleanup must reach the failing commit callback")
	require.Equal(t, codes.Unavailable, status.Code(err))
	_, stillPresent, err := receiptStore.Lookup(testCtx, org, "expired", business.SourceOperationMethod)
	require.NoError(t, err)
	require.True(t, stillPresent, "failed receipt commit must roll back cleanup")
	first, err := invoke()
	require.NoError(t, err)
	require.EqualValues(t, 1, first.(*gen.PruneSourceOperationReceiptsResponse).Receipts)
	replay, err := invoke()
	require.NoError(t, err)
	require.True(t, proto.Equal(first, replay))
	_, found, err := receiptStore.Lookup(testCtx, org, "expired", business.SourceOperationMethod)
	require.NoError(t, err)
	require.False(t, found)
	for _, item := range []struct{ org, effect string }{{org, "recent"}, {org, "other-source"}, {otherOrg, "foreign"}} {
		_, found, err := receiptStore.Lookup(testCtx, item.org, item.effect, business.SourceOperationMethod)
		require.NoError(t, err)
		require.True(t, found, item.effect)
	}
	for _, effect := range []string{"expired", "uncertain"} {
		attempt, err := svc.SourceOperationAttemptForActor(requestCtx, actor, org, effect)
		require.NoError(t, err)
		require.NotNil(t, attempt)
		ctx := receipts.WithEffect(requestCtx, receipts.Effect{ID: effect, Tenant: org, Method: business.SourceOperationMethod, RequestDigest: attempt.RequestDigest})
		_, err = svc.InvokeSourceOperation(ctx, actor, org, source.ID, d.Name, []byte(`{"id":"a"}`), func(context.Context, *business.SourceOperationResult) error {
			t.Fatal("expired mutation redispatched")
			return nil
		})
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.Contains(t, err.Error(), "outcome is unknown")
	}
	require.NoError(t, testStore.WithOrgTx(requestCtx, org, func(ctx context.Context) error {
		removed, err := testStore.PruneSourceOperationReceipts(ctx, otherOrg, foreign.ID, time.Now())
		require.NoError(t, err)
		require.Zero(t, removed, "RLS must deny pruning another tenant")
		return nil
	}))
	window, err := business.SourceReceiptRetentionWindow()
	require.NoError(t, err)
	require.Equal(t, 24*time.Hour+10*time.Minute, window)
}
