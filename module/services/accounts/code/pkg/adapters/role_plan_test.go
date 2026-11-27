package adapters

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"accounts/fixtures"
	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	policyv1 "accounts/pkg/gen/saas/policy/v1"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
)

// The role plan: every procedure this service declares, against the fixture
// people who can call it, with no host running.
//
// What each person may do is read off the procedure's DECLARED policy and
// nothing else (rolePlanFor). What actually happens is the real tenant
// admission chain — authentication, then the central floor — in front of a
// handler that records whether it was reached. A cell the plan refuses is
// then held to one of two deciders, and the test fails when it has neither:
//
//   - admission refused the call, which is a real outcome here; or
//   - admission passed it on, and the handler that serves it enforces the
//     capability that refuses this person (the same source scan
//     TestDeclaredPolicyEnforcedInLockstep reads), or names the permission
//     rule it decides (business.PermissionDecidedByHandler).
//
// The handler-decided cells of the operations people actually administer are
// then proved with real outcomes on the real store, in
// pkg/business/access_matrix_test.go.

// rolePlanPerson supplies the session identity read by tenant admission.
type rolePlanPerson struct {
	name         string
	signedIn     bool
	orgRole      gen.OrgRole // UNSPECIFIED: no membership in the session's organization
	platformRole string
}

var rolePlanOrgID = uuid.MustParse("019f6bf7-5b4b-74e5-8c17-092259bb1661")

func rolePlanPeople(t *testing.T) []rolePlanPerson {
	t.Helper()
	path, err := fixtures.FixturePath("dev-admin")
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var fixture struct {
		Users []struct {
			Name, Email, Role string
			Token             string `yaml:"provider_id"`
		}
		Organizations []struct {
			Owner   string
			Members []struct{ Email, Role string }
		}
	}
	require.NoError(t, yaml.Unmarshal(data, &fixture))
	var people []rolePlanPerson
	for _, user := range fixture.Users {
		if !strings.HasPrefix(user.Token, "dev-") {
			continue
		}
		person := rolePlanPerson{name: user.Name, signedIn: true}
		if user.Role == "super_admin" {
			person.platformRole = user.Role
		}
		for _, org := range fixture.Organizations {
			if org.Owner == user.Email {
				person.orgRole = gen.OrgRole_ORG_ROLE_OWNER
			}
			for _, member := range org.Members {
				if member.Email == user.Email {
					person.orgRole = gen.OrgRole(gen.OrgRole_value["ORG_ROLE_"+strings.ToUpper(member.Role)])
				}
			}
		}
		if user.Token == "dev-dana" {
			require.Empty(t, person.platformRole)
			require.Equal(t, gen.OrgRole_ORG_ROLE_MEMBER, person.orgRole)
		}
		people = append(people, person)
	}
	require.Len(t, people, 5)
	return append(people, rolePlanPerson{name: "a signed-in nonmember", signedIn: true}, rolePlanPerson{name: "a signed-out visitor"})
}

// rolePlanRefusal is why the declared policy refuses a person: the capability
// a handler must enforce for the refusal to be the handler's, and the
// permissions whose absence refuses them.
type rolePlanRefusal struct {
	reason      string
	capability  capability
	permissions []string
}

// rolePlanFor reads one cell of the plan off the declaration: nil admits.
func rolePlanFor(policy business.RPCPolicy, person rolePlanPerson) *rolePlanRefusal {
	p := policy.MethodPolicy
	switch p.GetExposure() {
	case policyv1.Exposure_EXPOSURE_PUBLIC:
		return nil
	case policyv1.Exposure_EXPOSURE_AUTHENTICATED:
	default:
		return &rolePlanRefusal{reason: "the procedure is not exposed to people"}
	}
	if !person.signedIn {
		return &rolePlanRefusal{reason: "not signed in"}
	}
	if p.GetPlatformRole() != policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE && person.platformRole == "" {
		return &rolePlanRefusal{reason: "holds no platform role", capability: capPlatform}
	}
	member := person.orgRole != gen.OrgRole_ORG_ROLE_UNSPECIFIED
	admin := person.orgRole == gen.OrgRole_ORG_ROLE_ADMIN || person.orgRole == gen.OrgRole_ORG_ROLE_OWNER
	switch p.GetTenant() {
	case policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_OWNER:
		if person.orgRole != gen.OrgRole_ORG_ROLE_OWNER {
			return &rolePlanRefusal{reason: "does not own the organization", capability: capOrgOwner}
		}
	case policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_ADMIN:
		if !member {
			return &rolePlanRefusal{reason: "not a member of the organization", capability: capOrgMember}
		}
		if !admin {
			return &rolePlanRefusal{reason: "does not administer the organization", capability: capOrgAdmin}
		}
	case policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_MEMBER,
		policyv1.TenantRequirement_TENANT_REQUIREMENT_TEAM_MEMBER:
		if !member {
			return &rolePlanRefusal{reason: "not a member of the organization", capability: capOrgMember}
		}
	}
	// A declared permission is held by an administrator, by a member when the
	// default member role carries it, and by nobody outside the organization.
	// A procedure that names a platform role and no tenant is the platform's:
	// its declared scope is an API-key ceiling, not an organization permission.
	if p.GetPlatformRole() != policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE {
		return nil
	}
	var missing []string
	for _, permission := range p.GetPermissions() {
		resource, action, _ := strings.Cut(permission, ":")
		if admin || (member && business.IsMemberPermission(resource, action)) {
			continue
		}
		missing = append(missing, permission)
	}
	if len(missing) > 0 {
		return &rolePlanRefusal{reason: "holds none of " + strings.Join(missing, ", "), permissions: missing}
	}
	return nil
}

// rolePlanStore answers the two identity questions admission asks about one
// person: their membership of the session's organization and their platform
// role. They hold no assigned role, so every permission check is the default
// member role's answer.
type rolePlanStore struct {
	business.Store
	person rolePlanPerson
	userID uuid.UUID
}

func (s *rolePlanStore) GetOrgMembership(_ context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	if s.person.orgRole == gen.OrgRole_ORG_ROLE_UNSPECIFIED || orgID != rolePlanOrgID.String() || userID != s.userID.String() {
		return nil, nil
	}
	return &gen.OrgMembership{UserId: userID, Role: s.person.orgRole}, nil
}

func (s *rolePlanStore) GetPlatformRole(context.Context, string) (string, error) {
	return s.person.platformRole, nil
}

func (s *rolePlanStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *rolePlanStore) CheckPermission(context.Context, string, gen.SubjectKind, string, string, string, string) (bool, string, error) {
	return false, "no matching permission found", nil
}

// admitThroughTenantAdmission drives the real tenant unary interceptor as one
// person and reports whether the handler behind it was reached.
func admitThroughTenantAdmission(t *testing.T, person rolePlanPerson, procedure string) (reached bool, err error) {
	t.Helper()
	userID := uuid.Must(uuid.NewV7())
	installEnforceService(t, &rolePlanStore{person: person, userID: userID})
	ctx := context.Background()
	if person.signedIn {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer any"))
	}
	interceptor := grpcAuthInterceptor(fixedMinter(&auth.Identity{
		UserID:    userID,
		OrgID:     rolePlanOrgID,
		SessionID: uuid.Must(uuid.NewV7()),
	}), rpcExposureTenant)
	_, err = interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: procedure},
		func(context.Context, any) (any, error) {
			reached = true
			return nil, nil
		})
	return reached, err
}

func TestRolePlanHoldsEveryProcedureForEveryPerson(t *testing.T) {
	enableEnforcement(t)
	scan, err := scanHandlers(".")
	require.NoError(t, err)
	allow := loadEnforcementAllowlist(t, "authz_enforcement_allowlist.json")

	deciders := map[string]int{}
	var procedures int
	people := rolePlanPeople(t)
	for _, policy := range business.RPCPolicies() {
		if policy.PolicyError != "" || !policy.Tier.Valid() {
			continue // refused as unclassified; TestReflectionRPCsAreDeniedAsUnclassified
		}
		procedures++
		for _, person := range people {
			t.Run(strings.TrimPrefix(policy.FullMethod, "/")+"/"+person.name, func(t *testing.T) {
				refusal := rolePlanFor(policy, person)
				reached, err := admitThroughTenantAdmission(t, person, policy.FullMethod)
				if refusal == nil {
					require.Truef(t, reached, "the declaration admits %s and admission refused: %v", person.name, err)
					deciders["admitted"]++
					return
				}
				if !reached {
					code := status.Code(err)
					require.Truef(t, code == codes.PermissionDenied || code == codes.Unauthenticated,
						"refused with %s, which is not a refusal a caller can read: %v", code, err)
					if !person.signedIn && policy.MethodPolicy.GetExposure() == policyv1.Exposure_EXPOSURE_AUTHENTICATED {
						require.Equal(t, codes.Unauthenticated, code, "a signed-out caller is told to sign in: %v", err)
					}
					deciders["refused at admission"]++
					return
				}
				// Admission passed it on: the handler must be the one that refuses.
				decider := handlerDecider(scan, allow, policy, refusal)
				require.NotEmptyf(t, decider,
					"%s (%s): the declaration refuses %s (%s), admission admitted them, and no handler gate refuses them",
					policy.FullMethod, policy.Tier, person.name, refusal.reason)
				deciders[decider]++
			})
		}
	}
	require.Equal(t, len(business.RPCPolicies()), procedures,
		"every declared procedure is in the plan; an unclassified one is refused for everyone")

	// The split is printed so a change that moves a decision from admission
	// to a handler, or to an exemption, is visible in the run.
	names := make([]string, 0, len(deciders))
	for name := range deciders {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Logf("%4d cells: %s", deciders[name], name)
	}
	require.NotZero(t, deciders["refused at admission"])
	require.NotZero(t, deciders["refused by the handler's own gate"])
}

// handlerDecider names what refuses a person admission passed on, or "" when
// nothing does.
func handlerDecider(scan handlerScan, allow map[string]enforcementExemption, policy business.RPCPolicy, refusal *rolePlanRefusal) string {
	// The host interface's Issue and GetDelegation carry their caller's
	// credential in the request body, which admission never reads: the adapter
	// authenticates it and resolves the organization from it
	// (business.ShapeAuthenticated; host_interface_stated_credential_test.go).
	if business.ShapeAuthenticated(policy.FullMethod) {
		return "authenticated by the adapter from the credential in the request"
	}
	if len(refusal.permissions) > 0 {
		if rule := business.PermissionDecidedByHandler(policy.FullMethod); rule != "" {
			return "refused by the handler's own gate"
		}
		return ""
	}
	if refusal.capability == "" {
		return ""
	}
	provided, found := scan.capabilitiesFor(policy.Method, policy.InputType)
	if found && (provided[refusal.capability] ||
		// A gate that requires an administrator refuses a non-member too.
		(refusal.capability == capOrgMember && provided[capOrgAdmin])) {
		return "refused by the handler's own gate"
	}
	if exemption, ok := allow[policy.FullMethod]; ok && (exemption.all || exemption.exempts(refusal.capability)) {
		return fmt.Sprintf("handler rule the declaration cannot express (%s)", "authz_enforcement_allowlist.json")
	}
	return ""
}
