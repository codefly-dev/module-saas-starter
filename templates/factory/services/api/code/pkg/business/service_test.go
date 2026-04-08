package business_test

import (
	"backend/pkg/business"
	"backend/pkg/gen"
	"backend/pkg/infra"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"

	"github.com/codefly-dev/core/sdk"
	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
)

// Shared test fixtures — initialized once in TestMain.
var (
	testStore   *infra.PostgresStore
	testService *business.Service
	testCtx     context.Context
	testCleanup func()
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	wool.SetGlobalLogLevel(wool.DEBUG)

	deps, err := sdk.WithDependencies(ctx,
		sdk.WithDebug(),
		sdk.WithNamingScope("test"),
		sdk.WithTimeout(90*time.Second),
		sdk.WithSilence("store"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WithDependencies failed: %v\n", err)
		os.Exit(1)
	}

	_, err = codefly.Init(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codefly.Init failed: %v\n", err)
		os.Exit(1)
	}

	store, err := infra.NewPostgresStore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewPostgresStore failed: %v\n", err)
		os.Exit(1)
	}

	service, err := business.NewService(store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewService failed: %v\n", err)
		os.Exit(1)
	}

	// Wire optional components
	vaultClient, err := infra.NewVaultClient(ctx)
	if err == nil {
		service.SetHasher(vaultClient)
	}

	tokenService, err := infra.NewTokenService(ctx)
	if err == nil {
		service.SetTokenSigner(tokenService)
	}

	auditEmitter := business.NewAsyncAuditEmitter(store, 1024)
	service.SetAuditEmitter(auditEmitter)

	entitlementChecker := business.NewDefaultEntitlementChecker(store)
	service.SetEntitlementChecker(entitlementChecker)

	testStore = store
	testService = service
	testCtx = ctx
	testCleanup = func() {
		auditEmitter.Close()
		store.Close()
		deps.Destroy(ctx)
	}

	code := m.Run()
	testCleanup()
	os.Exit(code)
}

// clearData resets test data between tests.
func clearData(t *testing.T) {
	t.Helper()
	err := testStore.ClearAll(testCtx)
	require.NoError(t, err)
}

func TestRegisterUser(t *testing.T) {
	clearData(t)

	resp, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "alice@test.com",
		Profile:      map[string]string{"name": "Alice"},
		Identity: &gen.UserIdentity{
			Provider:      "google",
			ProviderId:    "google-123",
			ProviderEmail: "alice@gmail.com",
			EmailVerified: true,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.User)
	require.Equal(t, "alice@test.com", resp.User.PrimaryEmail)
	require.Equal(t, gen.UserStatus_USER_STATUS_ACTIVE, resp.User.Status)
	require.NotEmpty(t, resp.User.Uuid)
	require.NotNil(t, resp.Identity)
	require.Equal(t, "google", resp.Identity.Provider)
	require.Equal(t, "google-123", resp.Identity.ProviderId)
}

func TestRegisterUser_DuplicateIdentity(t *testing.T) {
	clearData(t)

	_, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "first@test.com",
		Identity: &gen.UserIdentity{
			Provider:      "google",
			ProviderId:    "google-dup",
			ProviderEmail: "dup@gmail.com",
		},
	})
	require.NoError(t, err)

	_, err = testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "second@test.com",
		Identity: &gen.UserIdentity{
			Provider:      "google",
			ProviderId:    "google-dup",
			ProviderEmail: "dup2@gmail.com",
		},
	})
	require.Error(t, err)
}

func TestRegisterUser_CreatesDefaultOrg(t *testing.T) {
	clearData(t)

	resp, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "bob@test.com",
		Identity: &gen.UserIdentity{
			Provider:      "email",
			ProviderId:    "email-bob",
			ProviderEmail: "bob@test.com",
		},
	})
	require.NoError(t, err)

	resolved, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider:   "email",
		ProviderId: "email-bob",
	})
	require.NoError(t, err)
	require.True(t, resolved.Found)
	require.Equal(t, resp.User.Uuid, resolved.UserId)
	require.NotEmpty(t, resolved.OrgId, "user should have a default org")
	require.Contains(t, resolved.Roles, "admin", "user should be admin of their own org")
}

func TestCheckPermission_AdminWildcard(t *testing.T) {
	clearData(t)

	resp, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "admin@test.com",
		Identity: &gen.UserIdentity{
			Provider:      "email",
			ProviderId:    "email-admin",
			ProviderEmail: "admin@test.com",
		},
	})
	require.NoError(t, err)

	resolved, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider:   "email",
		ProviderId: "email-admin",
	})
	require.NoError(t, err)

	check, err := testService.CheckPermission(testCtx, &gen.CheckPermissionRequest{
		SubjectId:   resp.User.Uuid,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_USER,
		Resource:    "billing",
		Action:      "write",
		OrgId:       resolved.OrgId,
	})
	require.NoError(t, err)
	require.True(t, check.Allowed, "admin should have wildcard access: %s", check.Reason)
}

func TestResolveIdentity_NotFound(t *testing.T) {
	clearData(t)

	resolved, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider:   "nonexistent",
		ProviderId: "nobody",
	})
	require.NoError(t, err)
	require.False(t, resolved.Found)
}

func TestCreateOrganization(t *testing.T) {
	clearData(t)

	resp, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "orgowner@test.com",
		Identity: &gen.UserIdentity{
			Provider:      "email",
			ProviderId:    "email-orgowner",
			ProviderEmail: "orgowner@test.com",
		},
	})
	require.NoError(t, err)

	orgResp, err := testService.CreateOrganization(testCtx, resp.User.Uuid, &gen.CreateOrganizationRequest{
		Name: "Acme Corp",
		Slug: "acme-corp",
	})
	require.NoError(t, err)
	require.Equal(t, "Acme Corp", orgResp.Organization.Name)
	require.Equal(t, resp.User.Uuid, orgResp.Organization.OwnerId)
}

func TestTeamInheritedPermissions(t *testing.T) {
	clearData(t)

	// Register Alice and Bob
	_, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "alice@test.com",
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: "email-alice-team", ProviderEmail: "alice@test.com",
		},
	})
	require.NoError(t, err)

	bob, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "bob@test.com",
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: "email-bob-team", ProviderEmail: "bob@test.com",
		},
	})
	require.NoError(t, err)

	aliceResolved, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider: "email", ProviderId: "email-alice-team",
	})
	require.NoError(t, err)
	orgID := aliceResolved.OrgId

	// Add Bob to Alice's org, create team, add Bob to team
	err = testService.Store().AddOrgMember(testCtx, orgID, bob.User.Uuid, "member")
	require.NoError(t, err)

	teamResp, err := testService.CreateTeam(testCtx, &gen.CreateTeamRequest{
		OrgId: orgID, Name: "engineering", Description: "The engineering team",
	})
	require.NoError(t, err)

	err = testService.Store().AddTeamMember(testCtx, teamResp.Team.Id, bob.User.Uuid, "member")
	require.NoError(t, err)

	// Create deployer role, assign to team
	customRole, err := testService.CreateRole(testCtx, &gen.CreateRoleRequest{
		Name: "deployer", Description: "Can deploy", OrgId: orgID,
		Permissions: []*gen.Permission{{Resource: "deployments", Action: "write"}},
	})
	require.NoError(t, err)

	_, err = testService.AssignRole(testCtx, &gen.AssignRoleRequest{
		SubjectId: teamResp.Team.Id, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM,
		RoleId: customRole.Role.Id, OrgId: orgID,
	})
	require.NoError(t, err)

	// Bob should inherit via team
	check, err := testService.CheckPermission(testCtx, &gen.CheckPermissionRequest{
		SubjectId: bob.User.Uuid, SubjectKind: gen.SubjectKind_SUBJECT_KIND_USER,
		Resource: "deployments", Action: "write", OrgId: orgID,
	})
	require.NoError(t, err)
	require.True(t, check.Allowed, "Bob should inherit deploy via team: %s", check.Reason)

	// Charlie (not in team) should NOT have deploy permission
	charlie, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "charlie@test.com",
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: "email-charlie-team", ProviderEmail: "charlie@test.com",
		},
	})
	require.NoError(t, err)
	err = testService.Store().AddOrgMember(testCtx, orgID, charlie.User.Uuid, "member")
	require.NoError(t, err)

	check, err = testService.CheckPermission(testCtx, &gen.CheckPermissionRequest{
		SubjectId: charlie.User.Uuid, SubjectKind: gen.SubjectKind_SUBJECT_KIND_USER,
		Resource: "deployments", Action: "write", OrgId: orgID,
	})
	require.NoError(t, err)
	require.False(t, check.Allowed, "Charlie should NOT have deploy in Alice's org")
}

// ============================================================================
// Auth tests
// ============================================================================

func TestAuthenticate(t *testing.T) {
	clearData(t)

	resp, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider:      "google",
		ProviderId:    "google-auth-test",
		ProviderEmail: "auth@test.com",
		EmailVerified: true,
		Profile:       map[string]string{"name": "Auth Tester"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)
	require.NotEmpty(t, resp.RefreshToken)
	require.Equal(t, int64(900), resp.ExpiresIn)
	require.NotEmpty(t, resp.User.Uuid)
}

func TestAuthenticate_AutoRegister(t *testing.T) {
	clearData(t)

	// Authenticate with unknown identity — should auto-register
	resp, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider:      "github",
		ProviderId:    "github-new-user",
		ProviderEmail: "newuser@github.com",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.User.Uuid)

	// Verify the user now exists via ResolveIdentity
	resolved, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider:   "github",
		ProviderId: "github-new-user",
	})
	require.NoError(t, err)
	require.True(t, resolved.Found)
	require.Equal(t, resp.User.Uuid, resolved.UserId)
}

func TestRefreshToken(t *testing.T) {
	clearData(t)

	authResp, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider: "google", ProviderId: "google-refresh", ProviderEmail: "refresh@test.com",
	})
	require.NoError(t, err)

	refreshResp, err := testService.RefreshToken(testCtx, &gen.RefreshTokenRequest{
		RefreshToken: authResp.RefreshToken,
	})
	require.NoError(t, err)
	require.NotEmpty(t, refreshResp.AccessToken)
	require.NotEqual(t, authResp.RefreshToken, refreshResp.RefreshToken, "should rotate")

	// Old token should fail (consumed)
	_, err = testService.RefreshToken(testCtx, &gen.RefreshTokenRequest{
		RefreshToken: authResp.RefreshToken,
	})
	require.Error(t, err, "old refresh token should be rejected")
}

func TestLogout(t *testing.T) {
	clearData(t)

	authResp, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider: "google", ProviderId: "google-logout", ProviderEmail: "logout@test.com",
	})
	require.NoError(t, err)

	err = testService.Logout(testCtx, &gen.LogoutRequest{RefreshToken: authResp.RefreshToken})
	require.NoError(t, err)

	_, err = testService.RefreshToken(testCtx, &gen.RefreshTokenRequest{
		RefreshToken: authResp.RefreshToken,
	})
	require.Error(t, err, "refresh should fail after logout")
}

func TestGetJWKS(t *testing.T) {
	jwks, err := testService.GetJWKS(testCtx)
	require.NoError(t, err)
	require.Contains(t, jwks, "Ed25519")
	require.Contains(t, jwks, "EdDSA")
}
