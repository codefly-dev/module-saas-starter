package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/datasource/connector/connectortest"
	"accounts/pkg/datasource/operations"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	runnablev0 "github.com/codefly-dev/core/generated/go/codefly/runnable/v0"
	"github.com/codefly-dev/core/resources"
	"github.com/codefly-dev/core/runnable"
	"github.com/codefly-dev/sdk-go/receipts"
	"github.com/codefly-dev/sdk-go/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoregistry"
)

type sourceWireStore struct {
	business.Store
	source      *business.DatasourceSource
	declaration operations.Declaration
	attempt     *business.SourceOperationAttempt
	allowed     bool
}

func (s *sourceWireStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s *sourceWireStore) GetOrgMembership(context.Context, string, string) (*gen.OrgMembership, error) {
	return &gen.OrgMembership{Role: gen.OrgRole_ORG_ROLE_MEMBER}, nil
}
func (s *sourceWireStore) GetDatasourceSource(context.Context, string, string) (*business.DatasourceSource, error) {
	return s.source, nil
}
func (s *sourceWireStore) CanReadScopeNode(context.Context, string, string, gen.SubjectKind, string, string, string) (bool, error) {
	return s.allowed, nil
}
func (s *sourceWireStore) ListSourceOperations(context.Context, string, string) ([]operations.Declaration, error) {
	return []operations.Declaration{s.declaration}, nil
}
func (s *sourceWireStore) ReplaceSourceOperations(context.Context, string, string, []operations.Declaration) error {
	return nil
}
func (s *sourceWireStore) GetSourceOperationAttempt(_ context.Context, _ string, effect string) (*business.SourceOperationAttempt, error) {
	if s.attempt == nil || s.attempt.EffectID != effect {
		return nil, nil
	}
	return s.attempt, nil
}
func (s *sourceWireStore) CreateSourceOperationAttempt(context.Context, business.SourceOperationAttempt) (bool, error) {
	panic("replay dispatched")
}
func (s *sourceWireStore) DeleteSourceOperationAttempt(context.Context, string, string) error {
	panic("replay dispatched")
}
func (s *sourceWireStore) RecordSourceOperationReceipt(ctx context.Context, store receipts.Store, response proto.Message) error {
	return receipts.Record(ctx, store, sourceWireTx{}, response)
}

type sourceWireTx struct{}

func (sourceWireTx) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	panic("memory fixture must not execute SQL")
}

type sourceWireAudit struct{ events []business.AuditEntry }

func (a *sourceWireAudit) Emit(_ context.Context, e business.AuditEntry) {
	a.events = append(a.events, e)
}

func TestSourceOperationPreparedConnectJSONReplayAndCurrentAuthority(t *testing.T) {
	org, actor, source := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"
	declaration, err := operations.Admit(connectortest.OperationDeclaration())
	require.NoError(t, err)
	request := &gen.InvokeSourceOperationRequest{OrgId: org, SourceId: source, Operation: declaration.Name, InputJson: `{"id":"one"}`, EffectId: "effect-one"}
	digest, err := receipts.RequestDigest(request)
	require.NoError(t, err)
	store := &sourceWireStore{allowed: true, declaration: declaration, source: &business.DatasourceSource{ID: source, OrgID: org, Provider: business.DatasourceProviderAPI, API: &business.APIDatasourceConfig{}}, attempt: &business.SourceOperationAttempt{OrgID: org, ActorID: actor, SourceID: source, Operation: declaration.Name, DeclarationDigest: declaration.Digest, EffectID: request.EffectId, RequestDigest: digest}}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	previous, cache := service, orgMembershipCache
	service = svc
	orgMembershipCache = nil
	t.Cleanup(func() { service = previous; orgMembershipCache = cache })
	receiptStore := receipts.NewMemoryStore()
	require.NoError(t, svc.ConfigureSourceOperationReceipts(receiptStore))
	audit := &sourceWireAudit{}
	svc.SetAuditEmitter(audit)
	ctx := stampVerifiedIdentity(t.Context(), actor, org, auth.Assurance{})
	expected, err := sourceOperationResponse(&business.SourceOperationResult{Output: []byte(`{}`), EffectID: request.EffectId, CommittedAt: time.Now(), ProviderStatus: 200})
	require.NoError(t, err)
	effectCtx := receipts.WithEffect(ctx, receipts.Effect{ID: request.EffectId, Tenant: org, Method: business.SourceOperationMethod, RequestDigest: digest})
	require.NoError(t, receipts.Record(effectCtx, receiptStore, sourceWireTx{}, expected))
	handler := &datasourceConnectHandler{svc: svc}
	mux := http.NewServeMux()
	// The fixture supplies verified identity at the trust seam; the production
	// interceptor verifies JWT/Work Context and current delegation before this.
	stamp := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(_ context.Context, r connect.AnyRequest) (connect.AnyResponse, error) { return next(ctx, r) }
	})
	mux.Handle(business.SourceOperationMethod, connect.NewUnaryHandler(business.SourceOperationMethod, handler.InvokeSourceOperation, connect.WithInterceptors(stamp)))
	server := httptest.NewServer(mux)
	defer server.Close()
	pkg, spec, err := runnable.PackageFromMethod(protoregistry.GlobalFiles, &resources.RunnableLocation{Identity: &resources.RunnableIdentity{Name: "invoke-source", Module: "saas-starter", Workspace: "example", Version: "0.1.0"}, WorkspacePath: t.TempDir(), RelativeToWorkspace: "modules/saas-starter"}, runnable.ServiceOwner{Module: "saas-starter", Service: "accounts", Endpoint: "connect", Agent: &basev0.Agent{Kind: basev0.Agent_SERVICE, Name: "go-grpc", Publisher: "codefly.dev", Version: "0.1.53"}}, business.SourceOperationMethod)
	require.NoError(t, err)
	require.Nil(t, spec.Tool)
	scoped, err := spec.ResolveScopeSlots([]*runnablev0.ScopeSelection{{Slot: "source", Invoke: []*basev0.WorkScopeV1{{ResourceKind: "datasource.sources", Actions: []string{"invoke", "read"}, ResourceIds: []string{source}}}, Lookup: []*basev0.WorkScopeV1{{ResourceKind: "datasource.sources", Actions: []string{"read"}, ResourceIds: []string{source}}}}})
	require.NoError(t, err)
	encoded, err := runnable.EncodePrepared(&runnablev0.PreparedBinding{Operation: &runnablev0.PreparedOperation{Module: "saas-starter", Service: "accounts", Endpoint: "connect", Spelling: business.SourceOperationMethod}, Call: &runnablev0.PreparedCall{Address: server.URL, Route: &runnablev0.PreparedCall_Connect{Connect: &runnablev0.ConnectProcedure{Procedure: business.SourceOperationMethod}}}, Contract: pkg.Contract, Policy: scoped.Policy()})
	require.NoError(t, err)
	prepared, err := runnable.DecodePrepared(encoded)
	require.NoError(t, err)
	client := connect.NewClient[gen.InvokeSourceOperationRequest, gen.InvokeSourceOperationResponse](server.Client(), prepared.Call.Address+prepared.Call.GetConnect().Procedure, connect.WithProtoJSON())
	for range 2 {
		response, err := client.CallUnary(t.Context(), connect.NewRequest(proto.CloneOf(request)))
		require.NoError(t, err)
		require.True(t, proto.Equal(expected, response.Msg))
	}
	require.Len(t, audit.events, 2, "one observation per replay")
	different := proto.CloneOf(request)
	different.InputJson = `{"id":"two"}`
	_, err = client.CallUnary(t.Context(), connect.NewRequest(different))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	lookup, err := handler.LookupInvokeSourceOperation(ctx, connect.NewRequest(&gen.LookupInvokeSourceOperationRequest{EffectId: request.EffectId}))
	require.NoError(t, err)
	require.True(t, proto.Equal(expected, lookup.Msg))
	store.allowed = false
	_, err = client.CallUnary(t.Context(), connect.NewRequest(request))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = handler.LookupInvokeSourceOperation(ctx, connect.NewRequest(&gen.LookupInvokeSourceOperationRequest{EffectId: request.EffectId}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	store.allowed = true
	store.source.PersonalOwnerUserID = "another-member"
	_, err = client.CallUnary(t.Context(), connect.NewRequest(request))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	store.source.PersonalOwnerUserID = ""
	store.declaration.Path = "/changed/{id}"
	store.declaration.Digest = ""
	store.declaration, err = operations.Admit(store.declaration)
	require.NoError(t, err)
	_, err = client.CallUnary(t.Context(), connect.NewRequest(request))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	store.declaration = declaration
	store.attempt.EffectID = "unresolved"
	unknown, err := handler.LookupInvokeSourceOperation(ctx, connect.NewRequest(&gen.LookupInvokeSourceOperationRequest{EffectId: "unresolved"}))
	require.NoError(t, err)
	require.Equal(t, "unknown", unknown.Msg.Receipt.Status)
	require.Equal(t, "{}", unknown.Msg.OutputJson)
	require.Equal(t, "{}", unknown.Msg.Receipt.OutputJson)
	_, err = handler.LookupInvokeSourceOperation(ctx, connect.NewRequest(&gen.LookupInvokeSourceOperationRequest{EffectId: "missing"}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestSourceOperationJSONEnvelopeBoundAndScope(t *testing.T) {
	// Two output copies and JSON escaping must fit even at the declaration cap.
	payload, _ := json.Marshal(map[string]string{"value": strings.Repeat("\x01", (operations.MaxOutputBytes-20)/6)})
	require.LessOrEqual(t, len(payload), operations.MaxOutputBytes)
	_, err := sourceOperationResponse(&business.SourceOperationResult{Output: payload, EffectID: strings.Repeat("a", 256), CommittedAt: time.Now(), ProviderStatus: 200})
	require.NoError(t, err)
	request := &gen.InvokeSourceOperationRequest{InputJson: strings.Repeat("\x01", 12000)}
	require.False(t, sourceWireFits(request, sourceInputEnvelopeBytes))
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := workcontext.NewWorkContextSigner(workcontext.WorkContextSignerOptions{Issuer: "example.test", KeyID: "test-key", PrivateKey: key})
	require.NoError(t, err)
	_, claims, err := signer.StartTask(workcontext.StartTaskInput{Audience: business.SourceOperationAudience, TenantID: "org", OwnerPrincipalID: "owner", TaskID: "task", SessionID: "session", AuthorizationRevision: 1, ReplayPolicy: workcontext.WorkContextReplayIdempotent, AuthorityScopes: []*basev0.WorkScopeV1{{ResourceKind: "datasource.sources", Actions: []string{"invoke", "read"}, ResourceIds: []string{"source"}}}})
	require.NoError(t, err)
	ctx := context.WithValue(t.Context(), sourceOperationContextKey{}, claims)
	require.NoError(t, requireSourceOperationScope(ctx, "org", "source", "invoke"))
	require.Error(t, requireSourceOperationScope(ctx, "org", "other", "invoke"))
	claims.AuthorityScopes[0].ResourceIds = nil
	require.Error(t, requireSourceOperationScope(ctx, "org", "source", "invoke"))
}
