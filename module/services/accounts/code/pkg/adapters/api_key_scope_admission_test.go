package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

var (
	scopeAdmissionUserID = uuid.MustParse("019f6c03-0001-7000-8000-000000000001")
	scopeAdmissionOrgID  = uuid.MustParse("019f6c03-1001-7000-8000-000000000011")
)

// gatewayAssertion is what auth-gateway stamps on an admitted request: the
// identity, the credential kind, and the key's scope list — empty for a session
// and for a key created without scopes.
func gatewayAssertion(kind string, scopes ...string) http.Header {
	return http.Header{
		"X-Codefly-Gateway-Token": {"test-gateway-token"},
		"X-User-Id":               {scopeAdmissionUserID.String()},
		"X-Org-Id":                {scopeAdmissionOrgID.String()},
		"X-Credential-Kind":       {kind},
		"X-Scopes":                {strings.Join(scopes, ",")},
	}
}

func withGatewayToken(t *testing.T) {
	t.Helper()
	previous := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previous) })
}

// admitOverGRPC drives the real tenant unary interceptor and reports whether
// the handler beyond it ran.
func admitOverGRPC(t *testing.T, procedure string, assertion http.Header) (bool, error) {
	t.Helper()
	md := metadata.MD{}
	for key, values := range assertion {
		md.Append(strings.ToLower(key), values...)
	}
	executed := false
	_, err := grpcAuthInterceptor(nil, rpcExposureTenant)(metadata.NewIncomingContext(context.Background(), md), nil,
		&grpc.UnaryServerInfo{FullMethod: procedure},
		func(context.Context, any) (any, error) {
			executed = true
			return nil, nil
		})
	return executed, err
}

// connectAdmission serves every procedure behind the real Connect interceptor
// over a real HTTP server, so the procedure the interceptor sees is the one the
// client called.
type connectAdmission struct {
	server  *httptest.Server
	mu      sync.Mutex
	reached map[string]bool
}

func newConnectAdmission(t *testing.T, procedures []string) *connectAdmission {
	t.Helper()
	admission := &connectAdmission{reached: map[string]bool{}}
	mux := http.NewServeMux()
	for _, procedure := range procedures {
		procedure := procedure
		mux.Handle(procedure, connect.NewUnaryHandler(procedure,
			func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
				admission.mu.Lock()
				admission.reached[procedure] = true
				admission.mu.Unlock()
				return connect.NewResponse(&emptypb.Empty{}), nil
			},
			connect.WithInterceptors(connectAuthInterceptor(nil))))
	}
	admission.server = httptest.NewServer(mux)
	t.Cleanup(admission.server.Close)
	return admission
}

func (a *connectAdmission) call(t *testing.T, procedure string, assertion http.Header) (bool, error) {
	t.Helper()
	a.mu.Lock()
	delete(a.reached, procedure)
	a.mu.Unlock()
	request := connect.NewRequest(&emptypb.Empty{})
	for key, values := range assertion {
		for _, value := range values {
			request.Header().Add(key, value)
		}
	}
	_, err := connect.NewClient[emptypb.Empty, emptypb.Empty](a.server.Client(), a.server.URL+procedure).
		CallUnary(context.Background(), request)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reached[procedure], err
}

// userRPCs is every RPC that admits a signed-in caller: the ones an API key can
// reach at all.
func userRPCs(t *testing.T) []business.RPCPolicy {
	t.Helper()
	var policies []business.RPCPolicy
	for _, policy := range business.RPCPolicies() {
		if _, ok := business.LookupRPCPolicy(policy.FullMethod); ok && policy.Tier.RequiresUser() {
			policies = append(policies, policy)
		}
	}
	require.NotEmpty(t, policies)
	return policies
}

// An API key's authority is the scope list the RPC declares, on every RPC and
// every transport — not only on the handlers that remember to ask. An RPC that
// declares no scope has not been opened to API keys, so it refuses one; a key
// with no scopes has no authority; and a session is unchanged, because its
// authority is RBAC.
func TestAPIKeyAdmissionFollowsTheDeclaredScopesOnEveryRPC(t *testing.T) {
	withGatewayToken(t)
	withoutCentralEnforcement(t)

	policies := userRPCs(t)
	procedures := make([]string, 0, len(policies))
	for _, policy := range policies {
		procedures = append(procedures, policy.FullMethod)
	}
	overConnect := newConnectAdmission(t, procedures)

	transports := map[string]func(*testing.T, string, http.Header) (bool, error){
		"grpc":    admitOverGRPC,
		"connect": overConnect.call,
	}
	for name, admit := range transports {
		t.Run(name, func(t *testing.T) {
			for _, policy := range policies {
				declared := len(policy.Scopes) > 0

				executed, err := admit(t, policy.FullMethod, gatewayAssertion(credentialKindAPIKey, "*:*"))
				if declared {
					require.NoError(t, err, "%s declares %v; a root key must be admitted", policy.FullMethod, policy.Scopes)
					require.True(t, executed, policy.FullMethod)
				} else {
					require.Error(t, err, "%s declares no scope, so it must refuse an API key", policy.FullMethod)
					require.False(t, executed, policy.FullMethod)
				}

				if declared {
					executed, err = admit(t, policy.FullMethod, gatewayAssertion(credentialKindAPIKey, policy.Scopes...))
					require.NoError(t, err, "%s must admit a key carrying exactly its declared scope", policy.FullMethod)
					require.True(t, executed, policy.FullMethod)
				}

				executed, err = admit(t, policy.FullMethod, gatewayAssertion(credentialKindAPIKey))
				require.Error(t, err, "%s: a key with no scopes has no authority", policy.FullMethod)
				require.False(t, executed, policy.FullMethod)

				executed, err = admit(t, policy.FullMethod, gatewayAssertion(credentialKindSession))
				require.NoError(t, err, "%s: a session's admission is RBAC's, not scopes'", policy.FullMethod)
				require.True(t, executed, policy.FullMethod)
			}
		})
	}
}

// The escalations the audit named, spelled out: each is refused before a
// handler runs, while the key that does carry the declared scope is admitted.
func TestAPIKeyScopeEscalationsAreRefused(t *testing.T) {
	withGatewayToken(t)
	withoutCentralEnforcement(t)

	const (
		removeMember     = "/saas.accounts.v1.OrganizationService/RemoveMember"
		createInvitation = "/saas.accounts.v1.InvitationService/CreateInvitation"
		createAPIKey     = "/saas.accounts.v1.APIKeyService/CreateAPIKey"
	)
	overConnect := newConnectAdmission(t, []string{removeMember, createInvitation, createAPIKey})

	cases := []struct {
		name      string
		procedure string
		assertion http.Header
		admitted  bool
		code      codes.Code
	}{
		{"a webhooks:read key removes a member", removeMember, gatewayAssertion(credentialKindAPIKey, "webhooks:read"), false, codes.PermissionDenied},
		{"a webhooks:read key invites someone", createInvitation, gatewayAssertion(credentialKindAPIKey, "webhooks:read"), false, codes.PermissionDenied},
		{"a key with no scopes mints a key", createAPIKey, gatewayAssertion(credentialKindAPIKey), false, codes.PermissionDenied},
		{"an api_keys:write key mints a key", createAPIKey, gatewayAssertion(credentialKindAPIKey, "api_keys:write"), false, codes.PermissionDenied},
		{"a root key mints a key", createAPIKey, gatewayAssertion(credentialKindAPIKey, "*:*"), false, codes.PermissionDenied},
		{"a session mints a key", createAPIKey, gatewayAssertion(credentialKindSession), true, codes.OK},
		{"an invitations:write key invites someone", createInvitation, gatewayAssertion(credentialKindAPIKey, "invitations:write"), true, codes.OK},
		{"an invitations:* key invites someone", createInvitation, gatewayAssertion(credentialKindAPIKey, "invitations:*"), true, codes.OK},
		{"a session removes a member", removeMember, gatewayAssertion(credentialKindSession), true, codes.OK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executed, err := admitOverGRPC(t, tc.procedure, tc.assertion)
			require.Equal(t, tc.code, status.Code(err), "grpc: %v", err)
			require.Equal(t, tc.admitted, executed, "grpc")

			executed, err = overConnect.call(t, tc.procedure, tc.assertion)
			if tc.admitted {
				require.NoError(t, err, "connect")
			} else {
				require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "connect: %v", err)
			}
			require.Equal(t, tc.admitted, executed, "connect")
		})
	}
}

// requireScope, the handler-level ceiling, agrees with admission: a key with
// no scopes is refused rather than read as an interactive session.
func TestRequireScope_APIKeyWithoutScopesIsDenied(t *testing.T) {
	ctx := withCredentialKind(context.Background(), credentialKindAPIKey)
	require.Equal(t, codes.PermissionDenied, status.Code(requireScope(ctx, "users:read")))

	ctx = withScopes(withCredentialKind(context.Background(), credentialKindAPIKey), []string{"users:read"})
	require.NoError(t, requireScope(ctx, "users:read"))

	ctx = withCredentialKind(context.Background(), credentialKindSession)
	require.NoError(t, requireScope(ctx, "users:read"), "a session's authority is RBAC")
}

// The plain-HTTP routes behind billing and the subscription stream are not
// RPCs and declare no scope, so they refuse an API key whatever it carries.
func TestPlainHTTPRoutesRefuseAPIKeys(t *testing.T) {
	withGatewayToken(t)

	admit := func(assertion http.Header) error {
		request := httptest.NewRequest(http.MethodGet, SubscriptionStreamPath, nil)
		request.Header = assertion
		_, _, _, err := authenticateHTTPRequest(&business.Service{}, request)
		return err
	}
	require.Error(t, admit(gatewayAssertion(credentialKindAPIKey, "*:*")))
	require.Error(t, admit(gatewayAssertion(credentialKindAPIKey)))
	require.NoError(t, admit(gatewayAssertion(credentialKindSession)))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, SubscriptionStreamPath, nil)
	request.Header = gatewayAssertion(credentialKindAPIKey, "*:*")
	NewSubscriptionStreamHandler(&business.Service{}).ServeHTTP(recorder, request)
	require.Equal(t, http.StatusForbidden, recorder.Code, "a valid key that the route does not accept is forbidden, not unauthenticated")
}

// apiKeyMintStore answers the lookups CreateAPIKey's organization-admin gate
// makes and counts them; every other method is the embedded nil interface, so
// a handler that got further panics rather than returning a zero value.
type apiKeyMintStore struct {
	business.Store
	membershipReads int
}

func (s *apiKeyMintStore) GetPlatformRole(context.Context, string) (string, error) { return "", nil }

func (s *apiKeyMintStore) GetOrgMembership(context.Context, string, string) (*gen.OrgMembership, error) {
	s.membershipReads++
	return &gen.OrgMembership{UserId: scopeAdmissionUserID.String(), Role: gen.OrgRole_ORG_ROLE_ADMIN}, nil
}

// Only an interactive session creates API keys. A key that could would mint a
// copy of itself with no expiry — accounts cannot even see the calling key's
// own — so CreateAPIKey declares no API-key scope, and the handler refuses a key
// again before any other check: before the request is validated, before the
// mint ceiling, and before the organization is consulted.
func TestAPIKeyCannotCreateAPIKeys(t *testing.T) {
	policy, ok := business.LookupRPCPolicy("/saas.accounts.v1.APIKeyService/CreateAPIKey")
	require.True(t, ok)
	require.Empty(t, policy.Scopes, "CreateAPIKey must declare no API-key scope, so admission refuses every key")

	signedIn := func() context.Context {
		return stampVerifiedIdentity(context.Background(), scopeAdmissionUserID.String(), scopeAdmissionOrgID.String(), auth.Assurance{})
	}
	apiKeyCaller := func(scopes ...string) context.Context {
		return withScopes(withCredentialKind(signedIn(), credentialKindAPIKey), scopes)
	}
	session := func(scopes ...string) context.Context {
		return withScopes(withCredentialKind(signedIn(), credentialKindSession), scopes)
	}
	create := func(ctx context.Context, request *gen.CreateAPIKeyRequest) (int, error) {
		store := &apiKeyMintStore{}
		installLayeredAuthzService(t, store)
		_, err := (&APIKeyServer{}).CreateAPIKey(ctx, request)
		return store.membershipReads, err
	}
	requesting := func(requested ...*gen.Permission) *gen.CreateAPIKeyRequest {
		return &gen.CreateAPIKeyRequest{OrganizationId: scopeAdmissionOrgID.String(), Name: "minted-by-a-key", Scopes: requested}
	}
	root := &gen.Permission{Resource: "*", Action: "*"}
	usersRead := &gen.Permission{Resource: "users", Action: "read"}

	// The handler holds the rule on its own, whatever the key carries and asks
	// for: a malformed request is refused as a key's, not as malformed.
	for _, tc := range []struct {
		name    string
		caller  context.Context
		request *gen.CreateAPIKeyRequest
	}{
		{"a root key requesting a root key", apiKeyCaller("*:*"), requesting(root)},
		{"an api_keys:write key requesting nothing", apiKeyCaller("api_keys:write"), requesting()},
		{"an api_keys:write key requesting a scope it holds", apiKeyCaller("api_keys:write", "users:read"), requesting(usersRead)},
		{"an api_keys:* key requesting a narrower scope", apiKeyCaller("api_keys:*", "users:*"), requesting(usersRead)},
		{"a key with no scopes", apiKeyCaller(), requesting()},
		{"a key sending a malformed scope", apiKeyCaller("*:*"), requesting(&gen.Permission{Resource: "users", Action: "read,*:*"})},
		{"a key sending no organization", apiKeyCaller("*:*"), &gen.CreateAPIKeyRequest{Name: "minted-by-a-key"}},
		{"a caller whose credential kind is unknown", signedIn(), requesting()},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			reads, err := create(tc.caller, tc.request)
			require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
			require.Contains(t, status.Convert(err).Message(), "create API keys")
			require.Zero(t, reads, "the refusal must land before the organization is consulted")
		})
	}

	// A requested scope must be one scope, for a session too: the ceiling
	// compares the joined resource:action, so users / read,*:* would pass as a
	// scope under users:* and reach every service as users:read and the root *:*.
	for name, tc := range map[string]struct {
		caller    context.Context
		requested *gen.Permission
	}{
		"a comma smuggling a root scope in the action":   {session("api_keys:write", "users:*"), &gen.Permission{Resource: "users", Action: "read,*:*"}},
		"a comma smuggling a root scope in the resource": {session("api_keys:write", "users:*"), &gen.Permission{Resource: "*:*,users", Action: "read"}},
		"a colon in the action":                          {session(), &gen.Permission{Resource: "users", Action: "read:x"}},
		"whitespace in the action":                       {session(), &gen.Permission{Resource: "users", Action: "read *"}},
		"a comma from an unscoped session":               {session(), &gen.Permission{Resource: "users", Action: "read,*:*"}},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			reads, err := create(tc.caller, requesting(tc.requested))
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
			require.Zero(t, reads, "the refusal must land before the organization is consulted")
		})
	}

	// The mint ceiling still bounds a session that presented a scope list: it
	// may mint only what that list covers.
	for name, requested := range map[string]*gen.Permission{
		"a root key":                    root,
		"a scope the caller lacks":      {Resource: "users", Action: "write"},
		"a wildcard wider than granted": {Resource: "users", Action: "*"},
	} {
		t.Run("refuses a scoped session minting "+name, func(t *testing.T) {
			reads, err := create(session("api_keys:write", "users:read"), requesting(requested))
			require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
			require.Zero(t, reads, "the refusal must land before the organization is consulted")
		})
	}

	// A session continues to the organization-admin gate and on into the
	// domain, which fails later here for want of a key hasher.
	for _, tc := range []struct {
		name      string
		caller    context.Context
		requested []*gen.Permission
	}{
		{"a session requesting a root key", session(), []*gen.Permission{root}},
		{"a session requesting no scopes", session(), nil},
		{"a scoped session requesting a scope it holds", session("api_keys:write", "users:read"), []*gen.Permission{usersRead}},
		{"a scoped session requesting under its wildcard", session("api_keys:write", "users:*"), []*gen.Permission{usersRead}},
	} {
		t.Run("continues for "+tc.name, func(t *testing.T) {
			reads, err := create(tc.caller, requesting(tc.requested...))
			require.Error(t, err)
			require.NotEqual(t, codes.PermissionDenied, status.Code(err), "%v", err)
			require.Equal(t, 1, reads)
		})
	}
}

// requireScopesWithinCaller refuses a requested scope that is not one scope on
// its own, not only because the request contract does: the ceiling is where
// resource and action are joined, and a joined users / read,*:* compares as a
// scope under users:*.
func TestRequireScopesWithinCaller_RefusesAScopeThatIsNotOneScope(t *testing.T) {
	apiKey := withScopes(withCredentialKind(context.Background(), credentialKindAPIKey), []string{"api_keys:write", "users:*"})
	session := withCredentialKind(context.Background(), credentialKindSession)
	for name, requested := range map[string]*gen.Permission{
		"a comma in the action":    {Resource: "users", Action: "read,*:*"},
		"a comma in the resource":  {Resource: "*:*,users", Action: "read"},
		"a colon in the action":    {Resource: "users", Action: "read:x"},
		"whitespace in the action": {Resource: "users", Action: "read\t*"},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, codes.InvalidArgument, status.Code(requireScopesWithinCaller(apiKey, []*gen.Permission{requested})))
			require.Equal(t, codes.InvalidArgument, status.Code(requireScopesWithinCaller(session, []*gen.Permission{requested})))
		})
	}
	require.NoError(t, requireScopesWithinCaller(apiKey, []*gen.Permission{{Resource: "users", Action: "read"}}))
}

// storedKeyStore answers ValidateAPIKey's one lookup with a stored key.
type storedKeyStore struct {
	business.Store
	authentication *business.APIKeyAuthentication
}

func (s *storedKeyStore) GetAPIKeyAuthentication(context.Context, string) (*business.APIKeyAuthentication, error) {
	return s.authentication, nil
}

type storedKeyHasher struct{}

func (storedKeyHasher) HashKey(context.Context, string) (string, error) { return "hash", nil }

// A key's stored scopes reach accounts as the gateway's comma-joined X-Scopes,
// which parseScopes splits again. End to end: validate a stored key, join its
// scopes as the gateway does, and admit it to RevokeAPIKey (api_keys:write). A
// key stored as users / read,*:* must not come out the far side as the root
// *:*; a well-formed key comes out holding exactly what was stored.
func TestStoredScopeCannotWidenThroughTheGatewayJoin(t *testing.T) {
	withGatewayToken(t)
	withoutCentralEnforcement(t)
	const revokeAPIKey = "/saas.accounts.v1.APIKeyService/RevokeAPIKey"

	validate := func(t *testing.T, stored ...*gen.Permission) *gen.ValidateAPIKeyResponse {
		t.Helper()
		service, err := business.NewService(&storedKeyStore{authentication: &business.APIKeyAuthentication{
			Key:    &gen.APIKey{UserId: scopeAdmissionUserID.String(), OrganizationId: scopeAdmissionOrgID.String(), Scopes: stored},
			Claims: business.APIKeyIdentityClaims{Member: true},
		}})
		require.NoError(t, err)
		service.SetHasher(storedKeyHasher{})
		validated, err := service.ValidateAPIKey(context.Background(), "cfly_sk_live_example")
		require.NoError(t, err)
		return validated
	}

	for name, stored := range map[string]*gen.Permission{
		"a comma in the action":   {Resource: "users", Action: "read,*:*"},
		"a comma in the resource": {Resource: "*:*,users", Action: "read"},
	} {
		t.Run(name, func(t *testing.T) {
			validated := validate(t, stored)
			if validated.GetValid() {
				joined := gatewayAssertion(credentialKindAPIKey, validated.GetScopes()...)
				executed, err := admitOverGRPC(t, revokeAPIKey, joined)
				require.False(t, executed, "stored %s:%s reached api_keys:write as X-Scopes %q (%v)",
					stored.GetResource(), stored.GetAction(), joined.Get("X-Scopes"), err)
			}
			require.False(t, validated.GetValid(), "a key whose stored scope is not one scope must not authenticate")
		})
	}

	validated := validate(t, &gen.Permission{Resource: "users", Action: "read"}, &gen.Permission{Resource: "api_keys", Action: "write"})
	require.True(t, validated.GetValid())
	joined := gatewayAssertion(credentialKindAPIKey, validated.GetScopes()...)
	require.Equal(t, validated.GetScopes(), parseScopes(joined.Get("X-Scopes")), "the join round-trips a well-formed key")
	executed, err := admitOverGRPC(t, revokeAPIKey, joined)
	require.NoError(t, err)
	require.True(t, executed)
}

// The request contract (Permission's field rules) refuses a permission that is
// not one scope before any handler runs — for API keys and roles alike — and
// admits every resource and action the host, its seeds and its composed modules
// use: a name of letters, digits, `.`, `_` and `-`, or a lone `*`.
func TestPermissionContractAdmitsOnlyOneScope(t *testing.T) {
	asRole := func(permission *gen.Permission) error {
		return Validate(&gen.CreateRoleRequest{Name: "role", Permissions: []*gen.Permission{permission}})
	}
	asKey := func(permission *gen.Permission) error {
		return Validate(&gen.CreateAPIKeyRequest{OrganizationId: scopeAdmissionOrgID.String(), Name: "key", Scopes: []*gen.Permission{permission}})
	}
	for _, permission := range []*gen.Permission{
		{Resource: "*", Action: "*"},
		{Resource: "*", Action: "read"},
		{Resource: "users", Action: "*"},
		{Resource: "api_keys", Action: "write"},
		{Resource: "document_quarantine", Action: "release"},
		{Resource: "reference.console", Action: "read"},
		{Resource: "example-records", Action: "read"},
	} {
		require.NoError(t, asRole(permission), "%s:%s", permission.Resource, permission.Action)
		require.NoError(t, asKey(permission), "%s:%s", permission.Resource, permission.Action)
	}
	for _, permission := range []*gen.Permission{
		{Resource: "users", Action: "read,*:*"},
		{Resource: "*:*,users", Action: "read"},
		{Resource: "users", Action: "read:x"},
		{Resource: "users", Action: "read *"},
		{Resource: "users", Action: " read"},
		{Resource: "users", Action: "read\n"},
		{Resource: "users", Action: "read\x00"},
		{Resource: "users*", Action: "read"},
		{Resource: "", Action: "read"},
	} {
		require.Equal(t, codes.InvalidArgument, status.Code(asRole(permission)), "%q:%q", permission.Resource, permission.Action)
		require.Equal(t, codes.InvalidArgument, status.Code(asKey(permission)), "%q:%q", permission.Resource, permission.Action)
	}
}
