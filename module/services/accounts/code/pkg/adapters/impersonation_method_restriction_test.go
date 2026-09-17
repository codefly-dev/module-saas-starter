package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/gen/saas/accounts/v1/accountsv1connect"
	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

// The support operator, the user they step into, and that user's organization.
// The operator deliberately holds no membership: impersonation is how support
// reaches inside a tenant without joining it.
var (
	restrictionActorID  = uuid.MustParse("019f6c02-0001-7000-8000-000000000001")
	restrictionTargetID = uuid.MustParse("019f6c02-0002-7000-8000-000000000002")
	restrictionOrgID    = uuid.MustParse("019f6c02-1001-7000-8000-000000000011")
)

// impersonatingMinter and selfMinter differ in exactly one field, which is the
// whole point of the paired assertions below: a denial that survives when the
// acting-as id is removed is not a denial this feature caused.
func impersonatingMinter() func() auth.JWTMinter {
	return fixedMinter(&auth.Identity{
		UserID:         restrictionActorID,
		ActingAsUserID: restrictionTargetID,
		OrgID:          restrictionOrgID,
		SessionID:      uuid.Must(uuid.NewV7()),
	})
}

func selfMinter(userID uuid.UUID) func() auth.JWTMinter {
	return fixedMinter(&auth.Identity{
		UserID:    userID,
		OrgID:     restrictionOrgID,
		SessionID: uuid.Must(uuid.NewV7()),
	})
}

func fixedMinter(identity *auth.Identity) func() auth.JWTMinter {
	return func() auth.JWTMinter { return &fixedAccessMinter{identity: identity} }
}

// callThroughInterceptor drives the real tenant unary interceptor and reports
// whether the handler beyond it ran. The handler stands in for every mutating
// body: nothing it records can have happened if admission refused the call, so
// an empty record is the proof that the denial landed above the domain.
func callThroughInterceptor(t *testing.T, getMinter func() auth.JWTMinter, procedure string) (executed bool, err error) {
	t.Helper()
	interceptor := grpcAuthInterceptor(getMinter, rpcExposureTenant)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer any"))
	_, err = interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: procedure},
		func(context.Context, any) (any, error) {
			executed = true
			return nil, nil
		})
	return executed, err
}

func restrictedProcedures(t *testing.T) []string {
	t.Helper()
	var procedures []string
	for _, policy := range business.RPCPolicies() {
		if business.ImpersonationForbidden(policy) {
			procedures = append(procedures, policy.FullMethod)
		}
	}
	return procedures
}

// Shadow mode governs the central tenant floor, never this gate. Pinning it off
// keeps the admitted arms attributable to the impersonation decision alone.
func withoutCentralEnforcement(t *testing.T) {
	t.Helper()
	previous := centralEnforcement
	SetCentralEnforcementMode(false)
	t.Cleanup(func() { SetCentralEnforcementMode(previous) })
}

// The restriction is a property of the session, not of the user or the method:
// every restricted procedure is refused while acting as someone else, and the
// same procedure is admitted for the target acting for themself and for the
// operator acting for themself. Without those two admitted arms a denial here
// would be indistinguishable from an unrelated authorization failure.
func TestRestrictedProceduresAreDeniedOnlyWhileImpersonating(t *testing.T) {
	withoutCentralEnforcement(t)

	restricted := restrictedProcedures(t)
	require.Len(t, restricted, 48, "the declared restriction set changed; confirm it against AUTHZ_MATRIX.md")

	for _, procedure := range restricted {
		t.Run(procedure, func(t *testing.T) {
			executed, err := callThroughInterceptor(t, impersonatingMinter(), procedure)
			require.Error(t, err)
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Contains(t, status.Convert(err).Message(), "unavailable to an impersonated session")
			require.False(t, executed, "admission must refuse before the handler can mutate anything")

			executed, err = callThroughInterceptor(t, selfMinter(restrictionTargetID), procedure)
			require.NoError(t, err, "the target's own session must keep the procedure")
			require.True(t, executed)

			executed, err = callThroughInterceptor(t, selfMinter(restrictionActorID), procedure)
			require.NoError(t, err, "the operator's own session must keep the procedure")
			require.True(t, executed)
		})
	}
}

// The option is default-open: a procedure that declares no impersonation
// requirement stays reachable. Pinning both the undeclared value and the
// admission keeps a future regeneration from turning silence into a denial —
// or, read the other way, from letting a dropped declaration pass unnoticed.
func TestUndeclaredImpersonationRequirementRemainsAllowed(t *testing.T) {
	withoutCentralEnforcement(t)

	for _, procedure := range []string{
		"/saas.accounts.v1.UserService/GetSelf",
		"/saas.accounts.v1.APIKeyService/ListAPIKeys",
		"/saas.accounts.v1.PlatformAdminService/SearchUsers",
	} {
		t.Run(procedure, func(t *testing.T) {
			policy, ok := business.LookupRPCPolicy(procedure)
			require.True(t, ok)
			require.Equal(t,
				policyv1.ImpersonationRequirement_IMPERSONATION_REQUIREMENT_UNSPECIFIED,
				policy.MethodPolicy.GetImpersonation())
			require.False(t, business.ImpersonationForbidden(policy))

			executed, err := callThroughInterceptor(t, impersonatingMinter(), procedure)
			require.NoError(t, err)
			require.True(t, executed)
		})
	}
}

// StopImpersonation is the one procedure that must never join the restricted
// set: it ends the caller's own session, so refusing it while impersonating
// would leave the session no way out but to wait for the token to expire. It
// carries PLATFORM_ROLE_REQUIREMENT_NONE for the same reason — being
// impersonated is its authorization. This pins both halves against a future
// change that marks PlatformAdminService wholesale.
func TestStopImpersonationStaysReachableWhileImpersonating(t *testing.T) {
	withoutCentralEnforcement(t)

	const procedure = "/saas.accounts.v1.PlatformAdminService/StopImpersonation"
	policy, ok := business.LookupRPCPolicy(procedure)
	require.True(t, ok)
	require.False(t, business.ImpersonationForbidden(policy),
		"restricting the exit would strand an impersonated session until its token expires")
	require.Equal(t,
		policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE,
		policy.MethodPolicy.GetPlatformRole())

	executed, err := callThroughInterceptor(t, impersonatingMinter(), procedure)
	require.NoError(t, err)
	require.True(t, executed)
}

// The option defaults open; the lookup does not. An impersonated call whose
// policy will not resolve is refused rather than admitted as unrestricted.
func TestImpersonatedCallWithoutAResolvablePolicyIsDenied(t *testing.T) {
	identity, err := auth.ParseRequestIdentity(
		restrictionActorID.String(), restrictionTargetID.String(), restrictionOrgID.String(), "")
	require.NoError(t, err)
	ctx := stampRequestIdentity(context.Background(), identity, auth.Assurance{})

	err = enforceImpersonationPolicy(ctx, "/saas.accounts.v1.UserService/FutureUnclassifiedRPC")
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// An ordinary session reaches the same unclassified procedure through the
	// interceptor, which refuses it on its own default-deny classification.
	executed, err := callThroughInterceptor(t, selfMinter(restrictionTargetID),
		"/saas.accounts.v1.UserService/FutureUnclassifiedRPC")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.False(t, executed)
}

// Connect and gRPC must answer the impersonation question identically, or the
// restriction becomes a matter of which transport the caller picked. This arm
// runs the REAL handler over the real Connect stack so the denial is proven
// where it matters: the store is never reached, so no row can have changed.
func TestConnectTransportDeniesRestrictedProcedureWhileImpersonating(t *testing.T) {
	withoutCentralEnforcement(t)

	store := &impersonationWriteRecorder{}
	installLayeredAuthzService(t, store)

	mux := http.NewServeMux()
	mux.Handle(accountsv1connect.NewAPIKeyServiceHandler(
		&apiKeyConnectHandler{inner: &APIKeyServer{}},
		connect.WithInterceptors(connectAuthInterceptor(impersonatingMinter())),
	))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := accountsv1connect.NewAPIKeyServiceClient(server.Client(), server.URL)
	request := connect.NewRequest(&gen.CreateAPIKeyRequest{
		Name:           "escapes-the-impersonation-window",
		OrganizationId: restrictionOrgID.String(),
	})
	request.Header().Set("Authorization", "Bearer any")

	_, err := client.CreateAPIKey(context.Background(), request)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Contains(t, err.Error(), "unavailable to an impersonated session")
	require.Zero(t, store.reads, "admission must refuse before the handler reads any row")
}

// impersonationWriteRecorder counts the first store read the CreateAPIKey
// handler performs — the membership lookup behind requireOrgAdmin. Every other
// method is the embedded nil interface, so a handler that got further than the
// gate allows panics instead of quietly returning a zero value. A zero count is
// therefore evidence the request never reached the domain, not merely that it
// returned an error.
type impersonationWriteRecorder struct {
	business.Store
	reads int
}

func (s *impersonationWriteRecorder) GetOrgMembership(context.Context, string, string) (*gen.OrgMembership, error) {
	s.reads++
	return &gen.OrgMembership{UserId: restrictionTargetID.String(), Role: gen.OrgRole_ORG_ROLE_ADMIN}, nil
}
