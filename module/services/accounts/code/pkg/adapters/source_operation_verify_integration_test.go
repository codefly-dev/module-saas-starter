//go:build integration

package adapters

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"accounts/internal/testdb"
	"accounts/pkg/auth"
	ed25519minter "accounts/pkg/auth/ed25519"
	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/business"
	"accounts/pkg/datasource/apisource"
	"accounts/pkg/datasource/connector/connectortest"
	"accounts/pkg/datasource/operations"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/gen/saas/accounts/v1/accountsv1connect"
	"accounts/pkg/infra"
	"accounts/pkg/keyservice"

	"connectrpc.com/connect"
	"github.com/codefly-dev/core/sdk"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/codefly-dev/sdk-go/receipts"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Uses the same package lock and isolated Codefly dependency lifecycle as the
// business integration suite. No identity-stamping interceptor or seeded
// attempt/digest is involved: signed requests enter the production interceptor.
func TestSourceOperationVerifyPostgresIngress(t *testing.T) {
	_, err := testdb.RunWithPackageLock(func() int {
		t.Run("isolated owner dependencies", testSourceReceiptIngress)
		return 0
	})
	require.NoError(t, err)
}

func testSourceReceiptIngress(t *testing.T) {
	ctx := context.Background()
	deps, err := sdk.WithDependencies(ctx, sdk.WithExcludedDependencies("object-storage", "cache"),
		sdk.WithNamingScope("receipt-ingress-test"), sdk.WithTimeout(10*time.Minute), sdk.WithSilence("store"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, deps.Destroy(context.Background())) })
	_, err = codefly.Init(ctx)
	require.NoError(t, err)
	store, err := infra.NewPostgresStore(ctx)
	require.NoError(t, err)
	t.Cleanup(store.Close)
	svc, err := business.NewService(store)
	require.NoError(t, err)
	keys, err := keyservice.Load(ctx)
	require.NoError(t, err)
	svc.SetDatasourceConnector(keys.Cipher, nil, "")
	ticket, link := make([]byte, 32), make([]byte, 32)
	_, err = rand.Read(ticket)
	require.NoError(t, err)
	_, err = rand.Read(link)
	require.NoError(t, err)
	svc.SetDatasourceKeys(ticket, link)
	emitter, err := business.NewDurableAuditEmitter(store, store)
	require.NoError(t, err)
	t.Cleanup(emitter.Close)
	svc.SetAuditEmitter(emitter)
	require.NoError(t, store.WithControlPlane(ctx, func(ctx context.Context) error {
		return store.SyncAuditEventTypes(ctx, business.AuditEventCatalog())
	}))
	_, privateKey, err := ed25519minter.GenerateKey()
	require.NoError(t, err)
	minter := ed25519minter.New(ed25519minter.Config{Issuer: "receipt-test", Audience: "receipt-test"}, privateKey, pgauth.NewSessionStore(store))
	svc.SetJWTMinter(minter)
	resolver := pgauth.NewResolver(store)
	resolver.SetBootstrapAdminEmail("")
	svc.SetIdentityResolver(resolver)
	identity := func(name string) *auth.Identity {
		id, err := resolver.Resolve(ctx, &auth.Claims{Provider: "email", Subject: name,
			Email: name + "@test.invalid", EmailVerified: true}, auth.SignupIntent{OrganizationName: name})
		require.NoError(t, err)
		return id
	}
	owner, other := identity("receipt-owner"), identity("receipt-other")
	token := func(id *auth.Identity) string {
		pair, err := minter.Mint(ctx, id)
		require.NoError(t, err)
		return pair.AccessToken
	}
	ownerToken, otherToken := token(owner), token(other)
	org, actor := owner.OrgID.String(), owner.UserID.String()
	// The maintained fixture membership path supplies a second real member;
	// its signed session is then independently verified by ingress.
	require.NoError(t, svc.ConvergeFixtureOrgMember(ctx, &gen.AddOrgMemberRequest{
		OrgId: org, UserId: other.UserID.String(), Role: gen.OrgRole_ORG_ROLE_MEMBER}))
	member := *other
	member.OrgID, member.OrgRole = owner.OrgID, "member"
	member.SessionID = business.NewID()
	memberToken := token(&member)
	receiptStore, closeReceipts, err := store.NewEffectReceipts()
	require.NoError(t, err)
	t.Cleanup(func() { closeReceipts() })
	clock := &ingressReceiptClock{Store: receiptStore}
	require.NoError(t, svc.ConfigureSourceOperationReceipts(clock))
	previous := service
	WithService(svc)
	t.Cleanup(func() { WithService(previous) })
	servers, err := NewGrpServer(&Configuration{})
	require.NoError(t, err)
	endpoint := serveContractConnectTier(t, servers, "")
	client := accountsv1connect.NewDatasourceServiceClient(http.DefaultClient, "http://"+endpoint)
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "Bearer synthetic-provider-credential", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(provider.Close)
	// The existing owner test transport seam allows a loopback provider; the
	// production egress IP guard is qualified separately by apisource tests.
	svc.SetSourceOperationIntegrationClient(func(_ apisource.Config, credential string) business.APIOperationClient {
		return ingressProvider{credential: credential}
	})
	added, err := client.AddSource(ctx, signedReceiptRequest(&gen.AddSourceRequest{OrgId: org,
		Provider:   gen.DatasourceProvider_DATASOURCE_PROVIDER_API,
		Boundary:   &gen.AddSourceRequest_CollectionLabel{CollectionLabel: "Receipt ingress"},
		Credential: "synthetic-provider-credential",
		Config: &gen.AddSourceRequest_Api{Api: &gen.ApiDatasourceConfig{BaseUrl: provider.URL, ResourcePath: "/items",
			CredentialKind: gen.ApiCredentialKind_API_CREDENTIAL_KIND_BEARER}}}, ownerToken))
	require.NoError(t, err)
	source := added.Msg.Datasource
	var grant *gen.ScopeGrant
	require.NoError(t, store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		collections, err := store.ListCollectionAccess(ctx, org, "", 100, []string{"datasource"})
		if err != nil {
			return err
		}
		var path string
		for _, collection := range collections {
			if collection.GetNode().GetId() == source.BoundaryNodeId {
				path = collection.GetNode().GetScopePath()
			}
		}
		require.NotEmpty(t, path)
		role := business.NewIDString()
		if err := store.CreateRole(ctx, &gen.Role{Id: role, Name: "Receipt operations", OrgId: org,
			Permissions: []*gen.Permission{{Resource: "datasource", Action: "invoke"}, {Resource: "datasource", Action: "read"}}}); err != nil {
			return err
		}
		grant = &gen.ScopeGrant{Id: business.NewIDString(), OrgId: org, SubjectId: actor,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: path, RoleId: role, GrantedBy: actor}
		return store.GrantScope(ctx, grant)
	}))
	d := connectortest.OperationDeclaration()
	d.Effect, d.Method = operations.Mutation, "POST"
	declaration, err := declarationToProto(d)
	require.NoError(t, err)
	declared, err := client.DeclareSourceOperations(ctx, signedReceiptRequest(&gen.DeclareSourceOperationsRequest{
		OrgId: org, SourceId: source.Id, Operations: []*gen.SourceOperation{declaration}}, ownerToken))
	require.NoError(t, err)
	original := &gen.InvokeSourceOperationRequest{OrgId: org, SourceId: source.Id, Operation: d.Name,
		EffectId: "ingress-effect", InputJson: `{"id":"one"}`, ExpectedDeclarationDigest: proto.String(declared.Msg.Operations[0].Digest)}
	// Consume and discard a successful HTTP response at the caller boundary.
	// The caller sees a lost response, while the real Invoke transaction commits.
	lostClient := accountsv1connect.NewDatasourceServiceClient(&http.Client{Transport: discardInvokeResponse{}}, "http://"+endpoint)
	_, err = lostClient.InvokeSourceOperation(ctx, signedReceiptRequest(original, ownerToken))
	require.Error(t, err)
	require.EqualValues(t, 1, calls.Load())
	verified, err := client.VerifySourceOperationReceipt(ctx, signedReceiptRequest(original, ownerToken))
	require.NoError(t, err)
	require.Equal(t, "committed", verified.Msg.Receipt.Status)
	require.Equal(t, `{}`, verified.Msg.OutputJson)
	replayed, err := client.InvokeSourceOperation(ctx, signedReceiptRequest(original, ownerToken))
	require.NoError(t, err)
	require.True(t, proto.Equal(verified.Msg, replayed.Msg))
	require.EqualValues(t, 1, calls.Load(), "Invoke replay must not redispatch")

	// Actual generated REST -> gRPC -> Connect path, including production
	// Authorization forwarding, uses the same signed session and retained row.
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(restIdentityHeaderMatcher))
	require.NoError(t, gen.RegisterDatasourceServiceHandlerClient(ctx, mux, gen.NewDatasourceServiceClient(conn)))
	postEndpoint := httptest.NewServer(mux)
	t.Cleanup(postEndpoint.Close)
	postVerify := func(request *gen.InvokeSourceOperationRequest, bearer string) *httptest.ResponseRecorder {
		wire, err := protojson.Marshal(request)
		require.NoError(t, err)
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, postEndpoint.URL+"/v1/datasource-operation-receipts/verify", bytes.NewReader(wire))
		require.NoError(t, err)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+bearer)
		response, err := postEndpoint.Client().Do(r)
		require.NoError(t, err)
		defer response.Body.Close()
		w := httptest.NewRecorder()
		w.WriteHeader(response.StatusCode)
		_, err = io.Copy(w, response.Body)
		require.NoError(t, err)
		return w
	}
	w := postVerify(original, ownerToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	for _, change := range []func(*gen.InvokeSourceOperationRequest){
		func(r *gen.InvokeSourceOperationRequest) { r.InputJson = `{"id":"two"}` },
		func(r *gen.InvokeSourceOperationRequest) {
			r.ExpectedDeclarationDigest = proto.String("sha256:" + strings.Repeat("a", 64))
		},
		func(r *gen.InvokeSourceOperationRequest) { r.SourceId = business.NewIDString() },
		func(r *gen.InvokeSourceOperationRequest) { r.OrgId = other.OrgID.String() },
		func(r *gen.InvokeSourceOperationRequest) { r.ExpectedDeclarationDigest = nil },
		func(r *gen.InvokeSourceOperationRequest) {
			r.InputJson = strings.Repeat(" ", sourceInputEnvelopeBytes+1)
		},
	} {
		modified := proto.CloneOf(original)
		change(modified)
		require.Equal(t, http.StatusBadRequest, postVerify(modified, ownerToken).Code)
	}
	require.Equal(t, http.StatusNotFound, postVerify(original, otherToken).Code)
	require.Equal(t, http.StatusBadRequest, postVerify(original, memberToken).Code, "another actor cannot reuse this effect")
	require.Equal(t, http.StatusUnauthorized, postVerify(original, "invalid-token").Code)
	missing := proto.CloneOf(original)
	missing.EffectId = "never-invoked"
	require.Equal(t, http.StatusNotFound, postVerify(missing, ownerToken).Code)
	legacy, err := client.LookupInvokeSourceOperation(ctx, signedReceiptRequest(&gen.LookupInvokeSourceOperationRequest{EffectId: original.EffectId}, ownerToken))
	require.NoError(t, err)
	require.True(t, proto.Equal(verified.Msg, legacy.Msg))
	// Reopen the receipt pool over the same database, then rebuild its guard.
	// This is pool/guard restart coverage, not a full process restart claim.
	closeReceipts()
	reopened, closeReopened, err := store.NewEffectReceipts()
	require.NoError(t, err)
	t.Cleanup(func() { closeReopened() })
	clock.Store = reopened
	require.NoError(t, svc.ConfigureSourceOperationReceipts(clock))
	require.Equal(t, http.StatusOK, postVerify(original, ownerToken).Code)
	// Real Invoke persists an old receipt through the existing storage-clock
	// seam; public retention removes it while the permanent attempt survives.
	clock.before = time.Now().Add(-48 * time.Hour)
	expired := proto.CloneOf(original)
	expired.EffectId = "expired-effect"
	_, err = client.InvokeSourceOperation(ctx, signedReceiptRequest(expired, ownerToken))
	require.NoError(t, err)
	clock.before = time.Time{}
	_, err = client.PruneSourceOperationReceipts(ctx, signedReceiptRequest(&gen.PruneSourceOperationReceiptsRequest{OrgId: org, SourceId: source.Id, EffectId: "prune-effect"}, ownerToken))
	require.NoError(t, err)
	expiredResponse := postVerify(expired, ownerToken)
	require.Equal(t, http.StatusOK, expiredResponse.Code)
	require.Contains(t, expiredResponse.Body.String(), `"unknown"`)
	// Removing the declaration does not erase already committed evidence.
	_, err = client.DeclareSourceOperations(ctx, signedReceiptRequest(&gen.DeclareSourceOperationsRequest{OrgId: org, SourceId: source.Id}, ownerToken))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, postVerify(original, ownerToken).Code)
	require.NoError(t, store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		_, err := store.UpdateRole(ctx, grant.RoleId, org, "Read-only receipt access", []*gen.Permission{{Resource: "datasource", Action: "read"}})
		return err
	}))
	require.Equal(t, http.StatusOK, postVerify(original, ownerToken).Code, "verification requires read, not invoke")
	require.NoError(t, store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		return store.RevokeScope(ctx, org, actor, grant.SubjectKind, grant.ScopePath, grant.RoleId)
	}))
	require.Equal(t, http.StatusForbidden, postVerify(original, ownerToken).Code)
	require.EqualValues(t, 2, calls.Load(), "only the two distinct Invoke effects may dispatch")
}

func signedReceiptRequest[T any](message *T, token string) *connect.Request[T] {
	r := connect.NewRequest(message)
	r.Header().Set("Authorization", "Bearer "+token)
	return r
}

type ingressReceiptClock struct {
	receipts.Store
	before time.Time
}

func (c *ingressReceiptClock) Record(ctx context.Context, tx receipts.Tx, receipt receipts.Receipt) error {
	if !c.before.IsZero() {
		receipt.CommittedAt = c.before
	}
	return c.Store.Record(ctx, tx, receipt)
}

type discardInvokeResponse struct{}

func (discardInvokeResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return nil, io.ErrUnexpectedEOF
}

type ingressProvider struct{ credential string }

func (p ingressProvider) Do(ctx context.Context, method, target string, body []byte) (*apisource.Result, error) {
	r, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+p.credential)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 30 * time.Second}
	response, err := client.Do(r)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	return &apisource.Result{Body: data, StatusCode: response.StatusCode}, err
}
