package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/datasource/connector/connectortest"
	"accounts/pkg/datasource/operations"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/gen/saas/accounts/v1/accountsv1connect"
	policyv1 "accounts/pkg/gen/saas/policy/v1"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/receipts"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

func sourceVerificationFixture(t *testing.T) (*datasourceConnectHandler, context.Context, *gen.InvokeSourceOperationRequest, *sourceWireStore) {
	t.Helper()
	org, actor, source := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"
	declaration, err := operations.Admit(connectortest.OperationDeclaration())
	require.NoError(t, err)
	request := &gen.InvokeSourceOperationRequest{OrgId: org, SourceId: source, Operation: declaration.Name, InputJson: `{"id":"one"}`, EffectId: "effect-one", ExpectedDeclarationDigest: proto.String(declaration.Digest)}
	digest, err := receipts.RequestDigest(request)
	require.NoError(t, err)
	store := &sourceWireStore{allowed: true, declaration: declaration, source: &business.DatasourceSource{ID: source, OrgID: org, Provider: business.DatasourceProviderAPI, API: &business.APIDatasourceConfig{}}, attempt: &business.SourceOperationAttempt{OrgID: org, ActorID: actor, SourceID: source, Operation: declaration.Name, DeclarationDigest: declaration.Digest, EffectID: request.EffectId, RequestDigest: digest}}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	previous, cache := service, orgMembershipCache
	service, orgMembershipCache = svc, nil
	t.Cleanup(func() { service, orgMembershipCache = previous, cache })
	receiptStore := receipts.NewMemoryStore()
	require.NoError(t, svc.ConfigureSourceOperationReceipts(receiptStore))
	svc.SetAuditEmitter(&sourceWireAudit{})
	ctx := stampVerifiedIdentity(t.Context(), actor, org, auth.Assurance{})
	out, err := sourceOperationResponse(&business.SourceOperationResult{Output: []byte(`{"value":"retained"}`), EffectID: request.EffectId, CommittedAt: time.Now(), ProviderStatus: 200})
	require.NoError(t, err)
	effectCtx := receipts.WithEffect(ctx, receipts.Effect{ID: request.EffectId, Tenant: org, Method: business.SourceOperationMethod, RequestDigest: digest})
	require.NoError(t, receipts.Record(effectCtx, receiptStore, sourceWireTx{}, out))
	return &datasourceConnectHandler{svc: svc}, ctx, request, store
}

func TestSourceOperationVerifyExactRequest(t *testing.T) {
	h, ctx, original, store := sourceVerificationFixture(t)
	for _, tc := range []struct {
		name   string
		change func(*gen.InvokeSourceOperationRequest)
		code   connect.Code
	}{
		{"input", func(r *gen.InvokeSourceOperationRequest) { r.InputJson = `{"id":"two"}` }, connect.CodeFailedPrecondition},
		{"exact text", func(r *gen.InvokeSourceOperationRequest) { r.InputJson = `{ "id":"one"}` }, connect.CodeFailedPrecondition},
		{"source", func(r *gen.InvokeSourceOperationRequest) { r.SourceId = "44444444-4444-4444-8444-444444444444" }, connect.CodeFailedPrecondition},
		{"organization", func(r *gen.InvokeSourceOperationRequest) { r.OrgId = "44444444-4444-4444-8444-444444444444" }, connect.CodeFailedPrecondition},
		{"operation", func(r *gen.InvokeSourceOperationRequest) { r.Operation = "another_operation" }, connect.CodeFailedPrecondition},
		{"effect", func(r *gen.InvokeSourceOperationRequest) { r.EffectId = "another-effect" }, connect.CodeNotFound},
		{"declaration", func(r *gen.InvokeSourceOperationRequest) {
			r.ExpectedDeclarationDigest = proto.String("sha256:" + strings.Repeat("0", 64))
		}, connect.CodeFailedPrecondition},
		{"omitted declaration", func(r *gen.InvokeSourceOperationRequest) { r.ExpectedDeclarationDigest = nil }, connect.CodeInvalidArgument},
		{"empty declaration", func(r *gen.InvokeSourceOperationRequest) { r.ExpectedDeclarationDigest = proto.String("") }, connect.CodeInvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := proto.CloneOf(original)
			tc.change(request)
			response, err := h.VerifySourceOperationReceipt(ctx, connect.NewRequest(request))
			require.Equal(t, tc.code, connect.CodeOf(err))
			require.Nil(t, response)
			require.NotContains(t, err.Error(), "retained")
		})
	}
	for _, transport := range []connect.ClientOption{connect.WithProtoJSON(), connect.WithGRPC(), connect.WithGRPCWeb()} {
		stamp := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(_ context.Context, r connect.AnyRequest) (connect.AnyResponse, error) { return next(ctx, r) }
		})
		_, handler := accountsv1connect.NewDatasourceServiceHandler(h, connect.WithInterceptors(stamp))
		server := httptest.NewServer(handler)
		client := accountsv1connect.NewDatasourceServiceClient(server.Client(), server.URL, transport)
		for range 2 {
			response, err := client.VerifySourceOperationReceipt(t.Context(), connect.NewRequest(original))
			require.NoError(t, err)
			require.Equal(t, "committed", response.Msg.GetReceipt().GetStatus())
			require.JSONEq(t, `{"value":"retained"}`, response.Msg.OutputJson)
		}
		server.Close()
	}
	// An older generated server must report unsupported, never ordinary success.
	_, oldHandler := accountsv1connect.NewDatasourceServiceHandler(accountsv1connect.UnimplementedDatasourceServiceHandler{})
	old := httptest.NewServer(oldHandler)
	defer old.Close()
	_, err := accountsv1connect.NewDatasourceServiceClient(old.Client(), old.URL).VerifySourceOperationReceipt(ctx, connect.NewRequest(original))
	require.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
	// A legacy marker cannot be promoted by supplying its admitted digest later.
	legacy := proto.CloneOf(original)
	legacy.ExpectedDeclarationDigest = nil
	store.attempt.RequestDigest, err = receipts.RequestDigest(legacy)
	require.NoError(t, err)
	_, err = h.VerifySourceOperationReceipt(ctx, connect.NewRequest(original))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, err = h.LookupInvokeSourceOperation(ctx, connect.NewRequest(&gen.LookupInvokeSourceOperationRequest{EffectId: original.EffectId}))
	require.NoError(t, err)
}

func TestSourceOperationVerifyAuditPolicyMatchesRead(t *testing.T) {
	h, ctx, original, _ := sourceVerificationFixture(t)
	audit := &sourceWireAudit{}
	h.svc.SetAuditEmitter(audit)
	response, err := h.VerifySourceOperationReceipt(ctx, connect.NewRequest(original))
	require.NoError(t, err)
	require.Equal(t, "committed", response.Msg.GetReceipt().GetStatus())
	require.Empty(t, audit.events, "reading retained output must not record another invocation")

	// Source-scope refusals emit this event through the shared owner path, as
	// exercised with a real delegated Work Context by
	// TestSourceOperationDelegatedRefusalAudited/verify.
	policy, ok := business.LookupRPCPolicy(accountsv1connect.DatasourceServiceVerifySourceOperationReceiptProcedure)
	require.True(t, ok)
	require.Equal(t, policyv1.AuditEmission_AUDIT_EMISSION_FAILURE, policy.MethodPolicy.GetAudit().GetEmission())
	require.Equal(t, []string{string(business.EventDatasourceOperationInvoked)}, policy.MethodPolicy.GetAudit().GetEvents())
}

func TestSourceOperationVerifyCurrentAuthorityAndRetention(t *testing.T) {
	h, ctx, original, store := sourceVerificationFixture(t)
	// Read-only source authority suffices; the supplied source never grants it.
	read := context.WithValue(ctx, sourceOperationContextKey{}, &basev0.WorkContextV1{TenantId: original.OrgId, AuthorityScopes: []*basev0.WorkScopeV1{{ResourceKind: "datasource.sources", ResourceIds: []string{original.SourceId}, Actions: []string{"read"}}}})
	_, err := h.VerifySourceOperationReceipt(read, connect.NewRequest(original))
	require.NoError(t, err)
	for _, removed := range []bool{false, true} {
		if removed {
			store.declaration = operations.Declaration{}
		} else {
			store.declaration.Digest = "replacement"
		}
		out, err := h.VerifySourceOperationReceipt(read, connect.NewRequest(original))
		require.NoError(t, err)
		require.Equal(t, "committed", out.Msg.Receipt.Status)
	}
	store.allowed = false
	_, err = h.VerifySourceOperationReceipt(read, connect.NewRequest(original))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	store.allowed = true
	store.source.PersonalOwnerUserID = "another-actor"
	_, err = h.VerifySourceOperationReceipt(read, connect.NewRequest(original))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	store.source.PersonalOwnerUserID = ""
	other := stampVerifiedIdentity(t.Context(), "44444444-4444-4444-8444-444444444444", original.OrgId, auth.Assurance{})
	_, err = h.VerifySourceOperationReceipt(other, connect.NewRequest(original))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	otherTenant := stampVerifiedIdentity(t.Context(), store.attempt.ActorID, "55555555-5555-4555-8555-555555555555", auth.Assurance{})
	response, err := h.VerifySourceOperationReceipt(otherTenant, connect.NewRequest(original))
	require.Error(t, err)
	require.Nil(t, response)
	store.source = nil
	_, err = h.VerifySourceOperationReceipt(read, connect.NewRequest(original))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	store.source = &business.DatasourceSource{ID: original.SourceId, OrgID: original.OrgId}
	// Output expiry leaves the permanent marker and never calls a provider.
	require.NoError(t, h.svc.ConfigureSourceOperationReceipts(receipts.NewMemoryStore()))
	unknown, err := h.VerifySourceOperationReceipt(read, connect.NewRequest(original))
	require.NoError(t, err)
	require.Equal(t, "unknown", unknown.Msg.Receipt.Status)
	require.Equal(t, "{}", unknown.Msg.OutputJson)
	store.attempt = nil
	_, err = h.VerifySourceOperationReceipt(read, connect.NewRequest(original))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestSourceOperationVerifyNormalizationAndBounds(t *testing.T) {
	h, ctx, original, store := sourceVerificationFixture(t)
	request := connect.NewRequest(proto.CloneOf(original))
	request.Msg.EffectId = ""
	request.Header().Set(receipts.EffectIDHeaderName, original.EffectId)
	request.Header().Set(receipts.IdempotencyKeyHeaderName, original.EffectId)
	_, err := h.VerifySourceOperationReceipt(ctx, request)
	require.NoError(t, err)
	request.Msg.EffectId = "different"
	_, err = h.VerifySourceOperationReceipt(ctx, request)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	// Account for the complete normalized envelope, including declaration and
	// escaped JSON. At the exact bound the request remains readable.
	request = connect.NewRequest(proto.CloneOf(original))
	wire, err := sourceWireJSON.Marshal(request.Msg)
	require.NoError(t, err)
	request.Msg.InputJson += strings.Repeat(" ", sourceInputEnvelopeBytes-len(wire))
	wire, err = sourceWireJSON.Marshal(request.Msg)
	require.NoError(t, err)
	require.Len(t, wire, sourceInputEnvelopeBytes)
	store.attempt.RequestDigest, err = receipts.RequestDigest(request.Msg)
	require.NoError(t, err)
	_, err = h.VerifySourceOperationReceipt(ctx, request)
	require.NoError(t, err)
	request.Msg.InputJson += " "
	_, err = h.VerifySourceOperationReceipt(ctx, request)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSourceOperationVerifyPOSTGateway(t *testing.T) {
	h, ctx, original, store := sourceVerificationFixture(t)
	stamp := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(_ context.Context, r connect.AnyRequest) (connect.AnyResponse, error) { return next(ctx, r) }
	})
	_, handler := accountsv1connect.NewDatasourceServiceHandler(h, connect.WithInterceptors(stamp))
	endpoint := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	defer endpoint.Close()
	conn, err := grpc.NewClient(endpoint.Listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	mux := runtime.NewServeMux()
	require.NoError(t, gen.RegisterDatasourceServiceHandlerClient(t.Context(), mux, gen.NewDatasourceServiceClient(conn)))
	wire, err := sourceWireJSON.Marshal(original)
	require.NoError(t, err)
	post := httptest.NewRequest(http.MethodPost, "/v1/datasource-operation-receipts/verify", strings.NewReader(string(wire)))
	post.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, post)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "retained")
	bounded := proto.CloneOf(original)
	bounded.InputJson += strings.Repeat(" ", sourceInputEnvelopeBytes-len(wire))
	store.attempt.RequestDigest, err = receipts.RequestDigest(bounded)
	require.NoError(t, err)
	for _, excess := range []bool{false, true} {
		if excess {
			bounded.InputJson += " "
		}
		wire, err := sourceWireJSON.Marshal(bounded)
		require.NoError(t, err)
		post := httptest.NewRequest(http.MethodPost, "/v1/datasource-operation-receipts/verify", strings.NewReader(string(wire)))
		post.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, post)
		if excess {
			require.Equal(t, http.StatusBadRequest, response.Code)
		} else {
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		}
	}
	// Body:* admits no query-bound expectation, even beside a valid effect.
	query := httptest.NewRequest(http.MethodPost, "/v1/datasource-operation-receipts/verify?input_json=private&effect_id=effect-one", strings.NewReader("{}"))
	query.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	previousEmitter := emitHTTPFailureDiagnostic
	var diagnostic httpFailureDiagnostic
	emitHTTPFailureDiagnostic = func(d httpFailureDiagnostic) { diagnostic = d }
	t.Cleanup(func() { emitHTTPFailureDiagnostic = previousEmitter })
	logRequestOutcome(mux).ServeHTTP(response, query)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.NotContains(t, response.Body.String(), "private")
	require.NotContains(t, response.Body.String(), "retained")
	logged, err := json.Marshal(diagnostic)
	require.NoError(t, err)
	require.NotContains(t, string(logged), "private")
	require.NotContains(t, string(logged), "input_json")
	// Connect does not expose this secret-bearing read through its GET protocol.
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, accountsv1connect.DatasourceServiceVerifySourceOperationReceiptProcedure, nil))
	require.Equal(t, http.StatusMethodNotAllowed, response.Code)
	policy, ok := business.LookupRPCPolicy(accountsv1connect.DatasourceServiceVerifySourceOperationReceiptProcedure)
	require.True(t, ok)
	require.Equal(t, policyv1.Sensitivity_SENSITIVITY_SECRET, policy.MethodPolicy.GetRequestSensitivity())
	require.Equal(t, policyv1.Sensitivity_SENSITIVITY_SECRET, policy.MethodPolicy.GetResponseSensitivity())
	openapi, err := os.ReadFile("../../../generated/openapi-raw/api.swagger.json")
	require.NoError(t, err)
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(openapi, &spec))
	path := spec.Paths["/v1/datasource-operation-receipts/verify"]
	require.Len(t, path, 1)
	var operation struct {
		Parameters []struct {
			In string `json:"in"`
		} `json:"parameters"`
	}
	require.NoError(t, json.Unmarshal(path["post"], &operation))
	require.Len(t, operation.Parameters, 1)
	require.Equal(t, "body", operation.Parameters[0].In)
}

func TestSourceOperationVerifyStoredResponseBound(t *testing.T) {
	h, ctx, original, store := sourceVerificationFixture(t)
	receiptStore := receipts.NewMemoryStore()
	require.NoError(t, h.svc.ConfigureSourceOperationReceipts(receiptStore))
	effectCtx := receipts.WithEffect(ctx, receipts.Effect{ID: original.EffectId, Tenant: original.OrgId, Method: business.SourceOperationMethod, RequestDigest: store.attempt.RequestDigest})
	out := &gen.InvokeSourceOperationResponse{OutputJson: strings.Repeat("x", sourceOutputEnvelopeBytes)}
	require.NoError(t, receipts.Record(effectCtx, receiptStore, sourceWireTx{}, out))
	response, err := h.VerifySourceOperationReceipt(ctx, connect.NewRequest(original))
	require.Nil(t, response)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.EqualError(t, err, "internal: source receipt invalid")
}
