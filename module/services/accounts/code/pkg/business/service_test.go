package business_test

import (
	authcore "accounts/pkg/auth"
	ed25519minter "accounts/pkg/auth/ed25519"
	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"

	"github.com/codefly-dev/core/sdk"
	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"

	"accounts/internal/testdb"
)

// requestFixtureValidator is test-only wiring for business tests that exercise
// the explicitly enabled development login path. Production code never derives
// claims from an AuthenticateRequest; the real dev validator reads an allowlist
// from the selected fixture file.
type requestFixtureValidator struct {
	token  string
	claims *authcore.Claims
}

func (v *requestFixtureValidator) Validate(_ context.Context, token string) (*authcore.Claims, error) {
	if token != v.token {
		return nil, authcore.ErrUnknownIdentity
	}
	out := *v.claims
	return &out, nil
}

func authenticateFixture(ctx context.Context, req *gen.AuthenticateRequest) (*gen.AuthenticateResponse, error) {
	token := req.ProviderId
	if fixture := req.GetFixture(); fixture != nil && fixture.Token != "" {
		token = fixture.Token
	} else {
		// Keep older business tests concise while routing them through the
		// generated fixture oneof. Production ignores the deprecated field.
		req.Authentication = &gen.AuthenticateRequest_Fixture{Fixture: &gen.FixtureAuthentication{Token: token}}
	}
	email := req.ProviderEmail
	if email == "" {
		email = token + "@test.invalid"
	}
	testService.SetDevelopmentTokenValidator(&requestFixtureValidator{
		token: token,
		claims: &authcore.Claims{
			Provider:  req.Provider,
			Subject:   token,
			Email:     email,
			ExpiresAt: time.Now().Add(time.Hour),
		},
	})
	defer testService.SetDevelopmentTokenValidator(nil)
	return testService.Authenticate(ctx, req)
}

// Shared test fixtures — initialized once in TestMain.
var (
	testStore   *infra.PostgresStore
	testService *business.Service
	testCtx     context.Context
)

func TestMain(m *testing.M) {
	os.Exit(runBusinessTests(m))
}

func runBusinessTests(m *testing.M) int {
	ctx := context.Background()
	wool.SetGlobalLogLevel(wool.DEBUG)

	deps, err := sdk.WithDependencies(ctx,
		sdk.WithDebug(),
		// Keep this package-owned integration stack distinct from the outer
		// service runtime and the other package TestMain stacks. The SDK gives
		// each short-lived flow temporary host ports; the scope also isolates
		// its named containers and other runtime resources.
		sdk.WithNamingScope("business-test"),
		// A clean machine may need to pull Postgres, Vault, Redis, and MinIO
		// before the first integration test. Keep the dependency-start budget
		// separate from individual test timeouts so cold CI is deterministic.
		sdk.WithTimeout(5*time.Minute),
		sdk.WithSilence("store"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WithDependencies failed: %v\n", err)
		return 1
	}
	defer deps.Destroy(ctx)

	_, err = codefly.Init(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codefly.Init failed: %v\n", err)
		return 1
	}

	store, err := infra.NewPostgresStore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewPostgresStore failed: %v\n", err)
		return 1
	}
	defer store.Close()
	releasePackageLock, err := testdb.AcquirePackageLock(ctx, store.Pool())
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration test lock: %v\n", err)
		return 1
	}
	defer func() {
		if err := releasePackageLock(); err != nil {
			fmt.Fprintf(os.Stderr, "release integration test lock: %v\n", err)
		}
	}()

	service, err := business.NewService(store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewService failed: %v\n", err)
		return 1
	}
	service.SetWebhookJobProducer(store)

	// Wire optional components
	vaultClient, err := infra.NewVaultClient(ctx)
	if err == nil {
		service.SetHasher(vaultClient)
		service.SetMFASecretCipher(vaultClient)
	}

	// New auth pipeline: IdentityResolver + JWTMinter both backed by Postgres.
	sessionStore := pgauth.NewSessionStore(store)
	resolver := pgauth.NewResolver(store)
	_, priv, err := ed25519minter.GenerateKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "GenerateKey failed: %v\n", err)
		return 1
	}
	minter := ed25519minter.New(ed25519minter.Config{
		Issuer:   "saas-starter-test",
		Audience: "saas-starter-test",
	}, priv, sessionStore)
	service.SetIdentityResolver(resolver)
	service.SetJWTMinter(minter)
	webAuthnEngine, err := infra.NewWebAuthnEngine("localhost", "SaaS Starter Test", []string{"http://localhost:21931"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewWebAuthnEngine failed: %v\n", err)
		return 1
	}
	service.SetWebAuthnEngine(webAuthnEngine)

	auditEmitter, err := business.NewDurableAuditEmitter(store, store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewDurableAuditEmitter failed: %v\n", err)
		return 1
	}
	defer auditEmitter.Close()
	service.SetAuditEmitter(auditEmitter)

	entitlementChecker := business.NewDefaultEntitlementChecker(store)
	service.SetEntitlementChecker(entitlementChecker)

	testStore = store
	testService = service
	testCtx = ctx

	return m.Run()
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
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
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

	// Add Bob to Alice's org, create team, add Bob to team.
	// AddOrgMember writes to organization_members which is RLS-
	// protected; wrap in WithOrgTx so the policy passes.
	err = testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testService.Store().AddOrgMember(ctx, orgID, bob.User.Uuid, "member")
	})
	require.NoError(t, err)

	teamResp, err := testService.CreateTeam(testCtx, "test-actor", &gen.CreateTeamRequest{
		OrgId: orgID, Name: "engineering", Description: "The engineering team",
	})
	require.NoError(t, err)

	// team_members is RLS-protected (Phase 2C, JOIN-via-teams policy);
	// wrap in WithOrgTx so the insert sees the org context.
	err = testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testService.Store().AddTeamMember(ctx, teamResp.Team.Id, bob.User.Uuid, "member")
	})
	require.NoError(t, err)

	// Create deployer role, assign to team
	customRole, err := testService.CreateRole(testCtx, "test-actor", &gen.CreateRoleRequest{
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
		SubjectId: bob.User.Uuid, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
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
	err = testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testService.Store().AddOrgMember(ctx, orgID, charlie.User.Uuid, "member")
	})
	require.NoError(t, err)

	check, err = testService.CheckPermission(testCtx, &gen.CheckPermissionRequest{
		SubjectId: charlie.User.Uuid, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
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

	resp, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
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
	resp, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
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

	authResp, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
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

	authResp, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
		Provider: "google", ProviderId: "google-logout", ProviderEmail: "logout@test.com",
	})
	require.NoError(t, err)

	err = testService.Logout(testCtx, &gen.LogoutRequest{RefreshToken: authResp.RefreshToken}, "")
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
