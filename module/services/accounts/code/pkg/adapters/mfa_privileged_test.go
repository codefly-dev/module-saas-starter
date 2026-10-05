package adapters

import (
	"context"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The actor of these tests holds no verified MFA device. That is the whole
// point: the gate used to look for a device, find none, and admit — so the one
// principal whose compromise is the whole platform or the whole tenant was the
// one it could not touch.
type mfaPolicyStore struct {
	business.Store
	platformRole string
	orgRole      gen.OrgRole
	member       bool
}

func (s *mfaPolicyStore) GetPlatformRole(context.Context, string) (string, error) {
	return s.platformRole, nil
}

func (s *mfaPolicyStore) GetOrgMembership(context.Context, string, string) (*gen.OrgMembership, error) {
	if !s.member {
		return nil, nil
	}
	return &gen.OrgMembership{Role: s.orgRole}, nil
}

func (s *mfaPolicyStore) WithUserTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *mfaPolicyStore) HasVerifiedMFA(context.Context, string) (bool, error) {
	return false, nil
}

const (
	mfaActorID = "00000000-0000-4000-8000-0000000000a1"
	mfaOrgID   = "00000000-0000-4000-8000-0000000000b1"
)

func installMFAPolicyStore(t *testing.T, store *mfaPolicyStore) {
	t.Helper()
	previous := service
	t.Cleanup(func() { service = previous })
	svc, err := business.NewService(store)
	require.NoError(t, err)
	WithService(svc)
}

// The caller context every case shares: a verified identity for this actor in
// this organization, and no recent second factor.
func mfaRequestContext() context.Context {
	return auth.WithVerifiedDatabaseIdentity(context.Background(), mfaActorID, mfaOrgID)
}

func TestRequireMFARefusesUnenrolledPrivileged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store *mfaPolicyStore
	}{
		{"a platform support role", &mfaPolicyStore{platformRole: "support"}},
		{"a platform billing role", &mfaPolicyStore{platformRole: "billing"}},
		{"a platform super admin", &mfaPolicyStore{platformRole: "super_admin"}},
		{"an organization owner", &mfaPolicyStore{member: true, orgRole: gen.OrgRole_ORG_ROLE_OWNER}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installMFAPolicyStore(t, tc.store)

			err := requireMFA(mfaRequestContext(), mfaActorID)

			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.Equal(t, "mfa_required", status.Convert(err).Message())
		})
	}
}

// The controls. An ordinary member with nothing enrolled still passes, so the
// refusal above is the privilege and not a gate that refuses everybody; and an
// org admin is deliberately outside the rule, because admin is a role an owner
// grants and withdraws rather than the tenant's own lifecycle.
func TestRequireMFAStaysOptInForAnUnprivilegedActor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store *mfaPolicyStore
	}{
		{"a plain member", &mfaPolicyStore{member: true, orgRole: gen.OrgRole_ORG_ROLE_MEMBER}},
		{"an organization admin", &mfaPolicyStore{member: true, orgRole: gen.OrgRole_ORG_ROLE_ADMIN}},
		{"no membership at all", &mfaPolicyStore{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installMFAPolicyStore(t, tc.store)
			require.NoError(t, requireMFA(mfaRequestContext(), mfaActorID))
		})
	}
}
