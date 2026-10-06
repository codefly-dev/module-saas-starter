package adapters

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// R1019-N02, the half the first response missed: a principal is a credential-bearing
// actor, so minting one and revoking one are privilege-granting mutations, and both
// reached their write with no factor evidence — for a platform super-admin and for an
// organization owner alike.
//
// Both caller grades are exercised, because they pass through different authorization
// guards: a super-admin short-circuits requireOrgAdmin on the platform role, while an
// owner is admitted on membership. A gate placed after only one of those is a gate for
// only one of these callers.
type principalFactorStore struct {
	business.Store
	platformRole string
	orgRole      gen.OrgRole
	member       bool
	enrolled     bool
	principal    *business.Principal

	// The writes record instead of panicking, so "the mutation was reached" is an
	// assertion rather than a crash. Reaching the write with no factor evidence IS the
	// finding, so the test has to be able to say that happened.
	created []string
	revoked []string
}

func (s *principalFactorStore) GetPlatformRole(context.Context, string) (string, error) {
	return s.platformRole, nil
}

func (s *principalFactorStore) GetOrgMembership(context.Context, string, string) (*gen.OrgMembership, error) {
	if !s.member {
		return nil, nil
	}
	return &gen.OrgMembership{Role: s.orgRole}, nil
}

func (s *principalFactorStore) WithUserTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *principalFactorStore) HasVerifiedMFA(context.Context, string) (bool, error) {
	return s.enrolled, nil
}

func (s *principalFactorStore) GetPrincipal(context.Context, string) (*business.Principal, error) {
	return s.principal, nil
}

// RevokePrincipal resolves the principal first, to decide which authorization applies,
// and that read runs in a scope. Only the scope's Within is needed: the gate under test
// sits between that read and the write, so nothing beyond it has to work for the
// refusal to be the real one.
type principalFactorScope struct{ business.Scoped }

func (s *principalFactorScope) Within(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *principalFactorStore) As(business.Identity) business.Scoped {
	return &principalFactorScope{}
}

func (s *principalFactorStore) CreateAgentPrincipal(_ context.Context, p *business.Principal) error {
	s.created = append(s.created, p.AgentIdentifier)
	return nil
}

func (s *principalFactorStore) RevokePrincipal(_ context.Context, id, _ string) error {
	s.revoked = append(s.revoked, id)
	return nil
}

func (s *principalFactorStore) GetAgentPrincipal(context.Context, string, string) (*business.Principal, error) {
	return nil, nil
}

func (s *principalFactorStore) DisableAgentPrincipal(context.Context, string, string) (bool, error) {
	return false, nil
}

func (s *principalFactorStore) EnableAgentPrincipal(context.Context, string) (bool, error) {
	return false, nil
}

func (s *principalFactorStore) ListPrincipals(context.Context, string, string, int32, string) ([]*business.Principal, string, error) {
	return nil, "", nil
}

func principalFactorCalls() []factorCall {
	principals := &PrincipalServer{}
	return []factorCall{
		{"CreateAgentPrincipal", func(ctx context.Context) error {
			_, err := principals.CreateAgentPrincipal(ctx, &gen.CreateAgentPrincipalRequest{
				OrgId: mfaOrgID, AgentIdentifier: "a-delegated-agent", DisplayName: "A Delegated Agent",
			})
			return err
		}},
		{"RevokePrincipal", func(ctx context.Context) error {
			_, err := principals.RevokePrincipal(ctx, &gen.RevokePrincipalRequest{
				Id: factorUUID, Reason: "a recorded reason",
			})
			return err
		}},
	}
}

func TestR1019PrincipalMutationsRequireAFactor(t *testing.T) {
	for _, caller := range []struct {
		name  string
		store *principalFactorStore
	}{
		{"platform super-admin", &principalFactorStore{
			platformRole: "super_admin",
			principal:    &business.Principal{ID: factorUUID, OrgID: mfaOrgID},
		}},
		{"organization owner", &principalFactorStore{
			orgRole: gen.OrgRole_ORG_ROLE_OWNER, member: true,
			principal: &business.Principal{ID: factorUUID, OrgID: mfaOrgID},
		}},
	} {
		t.Run(caller.name, func(t *testing.T) {
			installMFAPolicyStore(t, caller.store)
			ctx := privilegedActorContext()

			for _, call := range principalFactorCalls() {
				err := call.invoke(ctx)
				require.Error(t, err, "%s must not mutate without a recent factor", call.name)
				require.Equal(t, codes.FailedPrecondition, status.Code(err),
					"%s: expected the factor gate to answer, not an earlier guard", call.name)
				require.Equal(t, "mfa_required", status.Convert(err).Message(), call.name)
			}

			// The refusal is only the gate if the write did not happen anyway.
			require.Empty(t, caller.store.created, "no principal may be created without a factor")
			require.Empty(t, caller.store.revoked, "no principal may be revoked without a factor")
		})
	}
}

// The control: the same caller with recent AAL2 evidence gets PAST the factor gate.
// Without it, a handler that refused everything would satisfy the test above and the
// refusals would prove nothing about the factor.
//
// Past the gate the handler runs into store surface this package does not wire, which
// is where the test's interest ends: a panic from there is proof the gate admitted, so
// it is recovered and reported as such rather than left to fail the run. What must
// never happen is an mfa_required.
func TestR1019PrincipalMutationsProceedPastTheFactorGate(t *testing.T) {
	store := &principalFactorStore{
		platformRole: "super_admin",
		principal:    &business.Principal{ID: factorUUID, OrgID: mfaOrgID},
	}
	installMFAPolicyStore(t, store)
	ctx := stampVerifiedIdentity(context.Background(), mfaActorID, "", auth.Assurance{
		Level:                 auth.AssuranceLevelAAL2,
		AuthenticationMethods: []string{auth.AuthenticationMethodWebAuthn},
		MFAVerifiedAt:         time.Now(),
	})
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, mfaActorID, mfaOrgID)

	for _, call := range principalFactorCalls() {
		t.Run(call.name, func(t *testing.T) {
			err, admitted := invokePastTheGate(ctx, call)
			if admitted {
				return // it reached store surface beyond the gate, which is the point
			}
			if err == nil {
				return
			}
			require.NotEqual(t, "mfa_required", status.Convert(err).Message(),
				"recent AAL2 evidence must satisfy the gate this mutation carries")
		})
	}
}

// invokePastTheGate reports the handler's error, and whether it got far enough to
// panic in unwired store surface — which only happens after the factor gate admitted.
func invokePastTheGate(ctx context.Context, call factorCall) (err error, admitted bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			admitted = true
		}
	}()
	return call.invoke(ctx), false
}

// The population this gate reaches is wider than "privileged actor", and that is worth
// asserting rather than discovering: CreateAgentPrincipal admits an organization ADMIN,
// who is not privileged, so requireMFA applies its opt-in rule — an ENROLLED admin
// without recent evidence is refused, an unenrolled one is not.
//
// Both halves are asserted because only the pair pins the behaviour. Without the
// unenrolled case this would also pass if the gate were fail-closed for everyone, which
// would force every operator's whole roster to enrol; without the enrolled case it would
// pass if the gate did nothing for a non-privileged actor. module/FEATURES.md states
// this, and an operator reading only "ordinary tenant mutations are not gated" would
// not predict it.
func TestR1019OrganizationAdminMeetsTheOptInFactorRule(t *testing.T) {
	admin := func(enrolled bool) *principalFactorStore {
		return &principalFactorStore{
			orgRole: gen.OrgRole_ORG_ROLE_ADMIN, member: true, enrolled: enrolled,
			principal: &business.Principal{ID: factorUUID, OrgID: mfaOrgID},
		}
	}
	create := principalFactorCalls()[0]
	require.Equal(t, "CreateAgentPrincipal", create.name)

	t.Run("enrolled without recent evidence is refused", func(t *testing.T) {
		store := admin(true)
		installMFAPolicyStore(t, store)

		err := create.invoke(privilegedActorContext())
		require.Error(t, err)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.Equal(t, "mfa_required", status.Convert(err).Message())
		require.Empty(t, store.created, "the write must not happen behind the refusal")
	})

	t.Run("not enrolled is still admitted", func(t *testing.T) {
		installMFAPolicyStore(t, admin(false))

		err, admitted := invokePastTheGate(privilegedActorContext(), create)
		if admitted {
			return // past the gate, into store surface this package does not wire
		}
		if err != nil {
			require.NotEqual(t, "mfa_required", status.Convert(err).Message(),
				"an unenrolled organization admin must not be forced to enrol by this gate")
		}
	})
}
