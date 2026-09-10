package adapters

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The support actor deliberately belongs to no organization: the whole point of
// impersonation is that support can act inside a tenant without joining it.
const (
	supportActorID  = "019f6c01-0001-7000-8000-000000000001"
	targetMemberID  = "019f6c01-0002-7000-8000-000000000002"
	targetAdminID   = "019f6c01-0003-7000-8000-000000000003"
	platformTargetI = "019f6c01-0004-7000-8000-000000000004"
	targetOrgID     = "019f6c01-1001-7000-8000-000000000011"
	foreignOrgID    = "019f6c01-1002-7000-8000-000000000012"
)

// impersonationStore answers only the reads the impersonation journey touches:
// the platform-role gate, the org-membership gate, and the tenant read the
// journey ends in. Every other method is the embedded nil interface, so a path
// that reached further than authorization allows panics instead of quietly
// returning a zero value.
type impersonationStore struct {
	business.Store
	platformRoles map[string]string
	memberships   map[string]gen.OrgRole

	mfaEnrolled map[string]bool

	platformRoleLookups []string
	listedOrgs          []string
	mfaProbedFor        []string
}

func (f *impersonationStore) GetPlatformRole(_ context.Context, userID string) (string, error) {
	f.platformRoleLookups = append(f.platformRoleLookups, userID)
	return f.platformRoles[userID], nil
}

func (f *impersonationStore) GetOrgMembership(_ context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	role, ok := f.memberships[orgID+"/"+userID]
	if !ok {
		return nil, nil
	}
	return &gen.OrgMembership{UserId: userID, Role: role}, nil
}

func (f *impersonationStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *impersonationStore) WithUserTx(ctx context.Context, userID string, fn func(context.Context) error) error {
	f.mfaProbedFor = append(f.mfaProbedFor, userID)
	return fn(ctx)
}

func (f *impersonationStore) HasVerifiedMFA(_ context.Context, userID string) (bool, error) {
	return f.mfaEnrolled[userID], nil
}

func (f *impersonationStore) ListOrgMembers(_ context.Context, orgID string) ([]*gen.OrgMembership, error) {
	f.listedOrgs = append(f.listedOrgs, orgID)
	return []*gen.OrgMembership{{UserId: targetMemberID, Role: gen.OrgRole_ORG_ROLE_MEMBER}}, nil
}

func targetTenantStore() *impersonationStore {
	return &impersonationStore{
		platformRoles: map[string]string{supportActorID: "support"},
		memberships: map[string]gen.OrgRole{
			targetOrgID + "/" + targetMemberID: gen.OrgRole_ORG_ROLE_MEMBER,
			targetOrgID + "/" + targetAdminID:  gen.OrgRole_ORG_ROLE_ADMIN,
		},
	}
}

// impersonatedContext is the context an authenticated impersonated request
// arrives with: the actor is accountable, the subject carries the authority.
func impersonatedContext(t *testing.T, actorID, subjectID, orgID string) context.Context {
	t.Helper()
	identity, err := auth.ParseRequestIdentity(actorID, subjectID, orgID, "")
	require.NoError(t, err)
	return stampRequestIdentity(context.Background(), identity, auth.Assurance{})
}

// The characterization the audit never executed: a support admin who is not a
// member of the target organization mints an impersonation token and performs a
// target-scoped read. The read must succeed on the target's membership, and the
// membership that was consulted must be the target's.
func TestImpersonatedRequestAuthorizesAsTargetNotActor(t *testing.T) {
	store := targetTenantStore()
	installLayeredAuthzService(t, store)

	ctx := impersonatedContext(t, supportActorID, targetMemberID, targetOrgID)

	// The authorization subject is the target; the actor stays recorded.
	actorID, err := requireAuth(ctx)
	require.NoError(t, err)
	require.Equal(t, targetMemberID, actorID)
	identity, ok := auth.VerifiedRequestIdentity(ctx)
	require.True(t, ok)
	require.Equal(t, supportActorID, identity.RealActorID())
	require.True(t, identity.Impersonated())

	// The user-scoped database context is the target's too, so RLS and the
	// membership cache resolve for the same subject the gate authorized.
	tenantID, dbUserID, ok := auth.VerifiedDatabaseIdentity(ctx)
	require.True(t, ok)
	require.Equal(t, targetOrgID, tenantID)
	require.Equal(t, targetMemberID, dbUserID)

	resp, err := (&OrgServer{}).ListMembers(ctx, &gen.ListOrgMembersRequest{OrgId: targetOrgID})
	require.NoError(t, err)
	require.Len(t, resp.GetMembers(), 1)
	require.Equal(t, []string{targetOrgID}, store.listedOrgs)
}

// The same support admin on their own session is denied the same read: the
// allow above comes from the target's membership, not from being support.
func TestSupportActorWithoutImpersonationCannotReadTargetTenant(t *testing.T) {
	store := targetTenantStore()
	installLayeredAuthzService(t, store)

	ctx := stampVerifiedIdentity(context.Background(), supportActorID, targetOrgID, auth.Assurance{})
	_, err := (&OrgServer{}).ListMembers(ctx, &gen.ListOrgMembersRequest{OrgId: targetOrgID})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Empty(t, store.listedOrgs, "authorization must fail before the tenant read")
}

// Impersonation confers exactly the target's authority: an ordinary member's
// session cannot perform an org-admin action, an admin's can.
func TestImpersonationConfersOnlyTargetAuthority(t *testing.T) {
	for _, tc := range []struct {
		name     string
		subject  string
		wantCode codes.Code
	}{
		{"member cannot administer", targetMemberID, codes.PermissionDenied},
		{"admin can administer", targetAdminID, codes.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installLayeredAuthzService(t, targetTenantStore())

			ctx := impersonatedContext(t, supportActorID, tc.subject, targetOrgID)
			err := requireOrgAdmin(ctx, tc.subject, targetOrgID)
			require.Equal(t, tc.wantCode, status.Code(err))
		})
	}
}

// Impersonating a platform administrator must not hand the support actor the
// target's platform powers, and the actor's own platform role must not follow
// them into the target's session either. Both directions are checked at the
// same gates.
func TestImpersonationNeverCarriesPlatformAuthority(t *testing.T) {
	for _, tc := range []struct {
		name        string
		actorRole   string
		subjectRole string
	}{
		{"target is a platform admin", "support", "super_admin"},
		{"actor is a platform admin", "super_admin", ""},
		{"both are platform admins", "super_admin", "super_admin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := targetTenantStore()
			store.platformRoles = map[string]string{
				supportActorID:  tc.actorRole,
				platformTargetI: tc.subjectRole,
			}
			installLayeredAuthzService(t, store)

			ctx := impersonatedContext(t, supportActorID, platformTargetI, targetOrgID)

			require.Equal(t, codes.PermissionDenied, status.Code(requirePlatformAdmin(ctx, platformTargetI)))
			require.Equal(t, codes.PermissionDenied, status.Code(requirePlatformRole(ctx, platformTargetI, "support")))

			// The super_admin bypass inside the org gates is the subtler leak:
			// the subject administers no organization, so the gate must deny
			// rather than resolve a platform role through a store lookup.
			require.Equal(t, codes.PermissionDenied, status.Code(requireOrgAdmin(ctx, platformTargetI, foreignOrgID)))
			require.Empty(t, store.platformRoleLookups,
				"an impersonated request must not resolve a platform role at all")
		})
	}
}

// Impersonation is not composable: a session already acting as someone else
// cannot mint a further impersonation token.
func TestNestedImpersonationIsDenied(t *testing.T) {
	store := targetTenantStore()
	store.platformRoles[targetMemberID] = "super_admin"
	installLayeredAuthzService(t, store)

	ctx := impersonatedContext(t, supportActorID, targetMemberID, targetOrgID)
	_, err := (&PlatformAdminServer{}).ImpersonateUser(ctx, &gen.ImpersonateUserRequest{UserId: targetAdminID})

	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Empty(t, store.platformRoleLookups)
}

// A caller-supplied acting-as header is spoofable and must be stripped unless
// the request also carries the gateway credential.
func TestForgedActingAsHeaderIsNotTrusted(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	minter := func() auth.JWTMinter {
		return &fixedAccessMinter{identity: &auth.Identity{
			UserID:    uuid.MustParse(supportActorID),
			SessionID: uuid.Must(uuid.NewV7()),
		}}
	}

	connectHeaders := http.Header{
		"Authorization":       []string{"Bearer any"},
		"X-User-Id":           []string{supportActorID},
		"X-Acting-As-User-Id": []string{targetMemberID},
	}
	ctx, err := (&connectPolicyInterceptor{getMinter: minter}).authorize(
		context.Background(), "/saas.accounts.v1.UserService/GetSelf", connectHeaders)
	require.NoError(t, err)
	require.False(t, auth.ImpersonatedRequest(ctx), "a caller-injected acting-as must not impersonate")

	grpcMD := metadata.Pairs(
		"authorization", "Bearer any",
		"x-user-id", supportActorID,
		"x-acting-as-user-id", targetMemberID,
	)
	ctx, err = (&grpcPolicyAuthorizer{getMinter: minter, exposure: rpcExposureTenant}).authorize(
		metadata.NewIncomingContext(context.Background(), grpcMD), "/saas.accounts.v1.UserService/GetSelf")
	require.NoError(t, err)
	require.False(t, auth.ImpersonatedRequest(ctx))
}

// A trusted-but-unparseable acting-as value is refused rather than admitted as
// "not impersonating", which would run the request with the actor's authority.
func TestMalformedForwardedActingAsIsRefused(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	connectHeaders := http.Header{
		"X-Codefly-Gateway-Token": []string{"test-gateway-token"},
		"X-User-Id":               []string{supportActorID},
		"X-Acting-As-User-Id":     []string{"not-a-uuid"},
	}
	_, err := (&connectPolicyInterceptor{getMinter: nil}).authorize(
		context.Background(), "/saas.accounts.v1.UserService/GetSelf", connectHeaders)
	require.Error(t, err)

	grpcMD := metadata.Pairs(
		"x-codefly-gateway-token", "test-gateway-token",
		"x-user-id", supportActorID,
		"x-acting-as-user-id", "not-a-uuid",
	)
	_, err = (&grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureTenant}).authorize(
		metadata.NewIncomingContext(context.Background(), grpcMD), "/saas.accounts.v1.UserService/GetSelf")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// One identity contract across transports: a locally verified token, a
// gateway-forwarded Connect request and a gateway-forwarded gRPC request all
// project the same actor, subject and database scope.
func TestImpersonationProjectionIsIdenticalAcrossTransports(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	sessionID := uuid.Must(uuid.NewV7())
	directMinter := func() auth.JWTMinter {
		return &fixedAccessMinter{identity: &auth.Identity{
			UserID:         uuid.MustParse(supportActorID),
			ActingAsUserID: uuid.MustParse(targetMemberID),
			OrgID:          uuid.MustParse(targetOrgID),
			SessionID:      sessionID,
		}}
	}

	directCtx, err := (&connectPolicyInterceptor{getMinter: directMinter}).authorize(
		context.Background(), "/saas.accounts.v1.UserService/GetSelf",
		http.Header{"Authorization": []string{"Bearer any"}})
	require.NoError(t, err)

	forwardedConnectCtx, err := (&connectPolicyInterceptor{getMinter: nil}).authorize(
		context.Background(), "/saas.accounts.v1.UserService/GetSelf", http.Header{
			"X-Codefly-Gateway-Token": []string{"test-gateway-token"},
			"X-User-Id":               []string{supportActorID},
			"X-Acting-As-User-Id":     []string{targetMemberID},
			"X-Org-Id":                []string{targetOrgID},
			"X-Session-Id":            []string{sessionID.String()},
		})
	require.NoError(t, err)

	forwardedGRPCCtx, err := (&grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureTenant}).authorize(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"x-codefly-gateway-token", "test-gateway-token",
			"x-user-id", supportActorID,
			"x-acting-as-user-id", targetMemberID,
			"x-org-id", targetOrgID,
			"x-session-id", sessionID.String(),
		)), "/saas.accounts.v1.UserService/GetSelf")
	require.NoError(t, err)

	for name, ctx := range map[string]context.Context{
		"direct token":       directCtx,
		"gateway to Connect": forwardedConnectCtx,
		"gateway to gRPC":    forwardedGRPCCtx,
	} {
		identity, ok := auth.VerifiedRequestIdentity(ctx)
		require.True(t, ok, name)
		require.Equal(t, supportActorID, identity.RealActorID(), name)
		require.Equal(t, targetMemberID, identity.EffectiveSubjectID(), name)
		require.Equal(t, targetOrgID, identity.OrgID.String(), name)

		tenantID, userID, ok := auth.VerifiedDatabaseIdentity(ctx)
		require.True(t, ok, name)
		require.Equal(t, targetOrgID, tenantID, name)
		require.Equal(t, targetMemberID, userID, name)

		got, ok := auth.VerifiedSessionID(ctx)
		require.True(t, ok, name)
		require.Equal(t, sessionID, got, name)

		principal, ok := wool.Get(ctx).UserID()
		require.True(t, ok, name)
		require.Equal(t, targetMemberID, principal, name)
	}
}

// REST reaches the same interceptor over a transcoding hop, so the hop has to
// carry the bearer and every identity header the interceptor trusts. Anything
// it forwards is still stripped without the gateway credential, which is why
// forwarding the whole trusted set is safe.
func TestRESTTranscodingCarriesTheTrustedIdentitySet(t *testing.T) {
	forwarded := append([]string{"Authorization"}, forwardedIdentityHeaders...)
	for _, header := range forwarded {
		key, ok := restIdentityHeaderMatcher(header)
		require.True(t, ok, header)
		require.Equal(t, strings.ToLower(header), key, header)
	}

	// The trust-control credential is not forwarded by this matcher: the REST
	// server annotates it explicitly, and the matcher must not become a second
	// way to assert gateway provenance.
	key, ok := restIdentityHeaderMatcher("X-Codefly-Gateway-Token")
	require.False(t, ok, "gateway credential must not ride the identity matcher, got %q", key)
}

// A target whose membership was revoked after the token was minted loses the
// authority the token was minted for: the gate re-reads the membership every
// request rather than trusting the session's organization claim.
func TestImpersonationHonoursStaleTargetMembership(t *testing.T) {
	store := targetTenantStore()
	delete(store.memberships, targetOrgID+"/"+targetMemberID)
	installLayeredAuthzService(t, store)

	ctx := impersonatedContext(t, supportActorID, targetMemberID, targetOrgID)
	_, err := (&OrgServer{}).ListMembers(ctx, &gen.ListOrgMembersRequest{OrgId: targetOrgID})

	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Empty(t, store.listedOrgs)
}

// The MFA gate follows the effective subject too: an impersonated session
// carries no step-up evidence, so a target with an enrolled second factor keeps
// their sensitive operations closed to support.
func TestImpersonatedSessionSatisfiesMFAAsTheTarget(t *testing.T) {
	store := targetTenantStore()
	store.mfaEnrolled = map[string]bool{targetMemberID: true}
	installLayeredAuthzService(t, store)

	ctx := impersonatedContext(t, supportActorID, targetMemberID, targetOrgID)
	err := requireMFA(ctx, targetMemberID)

	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, []string{targetMemberID}, store.mfaProbedFor,
		"the enrolment probed must be the effective subject's")
}

// The transport half of the same defect: a trusted gateway assertion naming an
// unusable actor alongside a real acting-as target must be denied on every
// transport. Before this was refused, the request installed the target as its
// sole principal, which reads as an ordinary session — so the target's platform
// role resolved and audit recorded the action as un-impersonated.
func TestForwardedActingAsWithoutUsableActorIsRefused(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	connectHeaders := http.Header{
		"X-Codefly-Gateway-Token": []string{"test-gateway-token"},
		"X-User-Id":               []string{"not-a-uuid"},
		"X-Acting-As-User-Id":     []string{targetMemberID},
	}
	_, err := (&connectPolicyInterceptor{getMinter: nil}).authorize(
		context.Background(), "/saas.accounts.v1.UserService/GetSelf", connectHeaders)
	require.Error(t, err)

	grpcMD := metadata.Pairs(
		"x-codefly-gateway-token", "test-gateway-token",
		"x-user-id", "not-a-uuid",
		"x-acting-as-user-id", targetMemberID,
	)
	_, err = (&grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureTenant}).authorize(
		metadata.NewIncomingContext(context.Background(), grpcMD), "/saas.accounts.v1.UserService/GetSelf")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
