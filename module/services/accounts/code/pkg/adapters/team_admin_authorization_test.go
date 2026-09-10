package adapters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

const (
	teamAdminActorID = "019f6c1a-2b30-7a41-8d55-1c2f4a9b6e70"
	teamAdminOrgID   = "019f6c1a-2b30-7a41-8d55-1c2f4a9b6e71"
	teamAdminTeamID  = "019f6c1a-2b30-7a41-8d55-1c2f4a9b6e72"
)

// teamAdminStore models one organization holding one team. orgRole is the
// actor's organization membership, where the zero value is the documented
// verified-nonmember answer; teamRole is a team_members row that may or may not
// still be legitimate.
type teamAdminStore struct {
	business.Store
	orgRole             gen.OrgRole
	orgMember           bool
	teamRole            gen.TeamRole
	teamMember          bool
	teamMembershipReads int
}

func (f *teamAdminStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *teamAdminStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *teamAdminStore) GetPlatformRole(context.Context, string) (string, error) { return "", nil }

func (f *teamAdminStore) GetTeamOrgID(_ context.Context, teamID string) (string, error) {
	if teamID == teamAdminTeamID {
		return teamAdminOrgID, nil
	}
	return "", nil
}

func (f *teamAdminStore) GetOrgMembership(_ context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	if !f.orgMember || orgID != teamAdminOrgID || userID != teamAdminActorID {
		return nil, nil
	}
	return &gen.OrgMembership{OrgId: orgID, UserId: userID, Role: f.orgRole}, nil
}

func (f *teamAdminStore) GetTeamMembership(_ context.Context, _, teamID, userID string) (*gen.TeamMembership, error) {
	f.teamMembershipReads++
	if !f.teamMember || teamID != teamAdminTeamID || userID != teamAdminActorID {
		return nil, nil
	}
	return &gen.TeamMembership{TeamId: teamID, UserId: userID, Role: f.teamRole}, nil
}

func installTeamAdminService(t *testing.T, store business.Store) {
	t.Helper()
	previous := service
	svc, err := business.NewService(store)
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })
}

func teamAdminContext() context.Context {
	return stampVerifiedIdentity(context.Background(), teamAdminActorID, teamAdminOrgID, auth.Assurance{})
}

// A team-admin row left behind by a historical removal must not, on its own,
// authorize anything. Team authority is derived from organization membership,
// so a verified nonmember is denied before the orphan is ever consulted —
// otherwise every caller of this helper would have to re-check organization
// membership for it to be safe.
func TestRequireTeamAdminDeniesVerifiedNonMemberHoldingLegacyTeamAdminRow(t *testing.T) {
	store := &teamAdminStore{
		orgMember:  false,
		teamMember: true,
		teamRole:   gen.TeamRole_TEAM_ROLE_ADMIN,
	}
	installTeamAdminService(t, store)

	orgID, err := requireTeamAdmin(teamAdminContext(), teamAdminActorID, teamAdminTeamID)

	require.Empty(t, orgID)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Zero(t, store.teamMembershipReads,
		"a verified nonmember is denied without consulting the stale team row")
}

// The nonmember answer denies whatever the orphan says, including a team-owner
// row, which is the strongest team authority there is.
func TestRequireTeamAdminDeniesVerifiedNonMemberHoldingLegacyTeamOwnerRow(t *testing.T) {
	store := &teamAdminStore{
		orgMember:  false,
		teamMember: true,
		teamRole:   gen.TeamRole_TEAM_ROLE_OWNER,
	}
	installTeamAdminService(t, store)

	_, err := requireTeamAdmin(teamAdminContext(), teamAdminActorID, teamAdminTeamID)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// The legitimate path is unchanged: a plain organization member who genuinely
// administers the team still passes, which is what separates this from simply
// requiring organization admin everywhere.
func TestRequireTeamAdminAdmitsOrgMemberWhoAdministersTheTeam(t *testing.T) {
	store := &teamAdminStore{
		orgMember:  true,
		orgRole:    gen.OrgRole_ORG_ROLE_MEMBER,
		teamMember: true,
		teamRole:   gen.TeamRole_TEAM_ROLE_ADMIN,
	}
	installTeamAdminService(t, store)

	orgID, err := requireTeamAdmin(teamAdminContext(), teamAdminActorID, teamAdminTeamID)
	require.NoError(t, err)
	require.Equal(t, teamAdminOrgID, orgID)
}

// An organization admin administers every team in the organization, including
// one with no members yet — the bootstrap case that keeps a freshly created
// team from being unmanageable.
func TestRequireTeamAdminAdmitsOrgAdminOfAnEmptyTeam(t *testing.T) {
	store := &teamAdminStore{orgMember: true, orgRole: gen.OrgRole_ORG_ROLE_ADMIN}
	installTeamAdminService(t, store)

	orgID, err := requireTeamAdmin(teamAdminContext(), teamAdminActorID, teamAdminTeamID)
	require.NoError(t, err)
	require.Equal(t, teamAdminOrgID, orgID)
}

// A member of the organization who is only a plain member of the team is still
// denied — the fail-closed nonmember branch did not widen anything.
func TestRequireTeamAdminDeniesPlainTeamMember(t *testing.T) {
	store := &teamAdminStore{
		orgMember:  true,
		orgRole:    gen.OrgRole_ORG_ROLE_MEMBER,
		teamMember: true,
		teamRole:   gen.TeamRole_TEAM_ROLE_MEMBER,
	}
	installTeamAdminService(t, store)

	_, err := requireTeamAdmin(teamAdminContext(), teamAdminActorID, teamAdminTeamID)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
