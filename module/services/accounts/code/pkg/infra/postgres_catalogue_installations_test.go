//go:build !pure

package infra_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// catalogueInstallations reads the platform Catalogue's installation listing the
// way the Service does — under the control plane — keyed by installation id, so
// installations other tests left in the shared database do not matter.
func catalogueInstallations(t *testing.T) map[string]*business.CatalogueInstallationRecord {
	t.Helper()
	var records []*business.CatalogueInstallationRecord
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		records, err = testStore.ListCatalogueInstallations(ctx)
		return err
	}))
	byID := make(map[string]*business.CatalogueInstallationRecord, len(records))
	for _, record := range records {
		byID[record.Installation.GetId()] = record
	}
	return byID
}

func seedCatalogueTeam(t *testing.T, orgID, name string) string {
	t.Helper()
	teamID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateTeam(ctx, &gen.Team{
			Id: teamID, OrgId: orgID, Name: name, Slug: "team-" + teamID, Path: "team-" + teamID,
		})
	}))
	return teamID
}

func grantCatalogueTeam(t *testing.T, orgID, teamID, roleID, path string, expiresAt *timestamppb.Timestamp) {
	t.Helper()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.GrantScope(ctx, &gen.ScopeGrant{
			Id: business.NewIDString(), OrgId: orgID, SubjectId: teamID,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM, ScopePath: path, RoleId: roleID, ExpiresAt: expiresAt,
		})
	}))
}

// The Catalogue lists an active installation with its organization's name and
// the release its agent was created for, and lists the teams an active grant
// reaches inside it in two kinds: granted at the installation's authority root,
// and inherited from a grant above it. Not the agent's own standing grant, not a
// team granted elsewhere, and not an expired grant.
func TestListCatalogueInstallationsReadsTeamsReachingTheRoot(t *testing.T) {
	orgID, _, roleID, installation, rootPath := installFixture(t, "doc", "read")

	exposed := seedCatalogueTeam(t, orgID, "Exposed Team")
	grantCatalogueTeam(t, orgID, exposed, roleID, rootPath, nil)

	elsewhere := seedCatalogueTeam(t, orgID, "Elsewhere Team")
	registerNode(t, orgID, "elsewhere", "space", "", "")
	grantCatalogueTeam(t, orgID, elsewhere, roleID, "elsewhere", nil)

	expired := seedCatalogueTeam(t, orgID, "Expired Team")
	grantCatalogueTeam(t, orgID, expired, roleID, rootPath, timestamppb.New(time.Now().Add(-time.Hour)))

	record := catalogueInstallations(t)[installation.Id]
	require.NotNil(t, record, "an active installation is listed")
	require.Equal(t, orgID, record.Installation.GetOrgId())
	require.Equal(t, "acme.example/solution", record.Installation.GetSolutionIdentifier())
	require.Equal(t, "Test Org", record.OrgName)
	require.Equal(t, "acme.example/solution:1.0.0", record.AgentIdentifier)

	require.Empty(t, record.InheritedTeams, "nothing above the root grants anything yet")
	require.Len(t, record.GrantedTeams, 1, "only the active team grant at the root is granted here")
	team := record.GrantedTeams[0]
	require.Equal(t, exposed, team.GetGrant().GetSubjectId())
	require.Equal(t, gen.SubjectKind_SUBJECT_KIND_TEAM, team.GetGrant().GetSubjectKind())
	require.Equal(t, orgID, team.GetGrant().GetOrgId())
	require.Equal(t, rootPath, team.GetGrant().GetScopePath())
	require.Equal(t, "Exposed Team", team.GetSubjectLabel())
	require.NotEmpty(t, team.GetRoleName())
}

// An uninstalled solution is no longer installed anywhere the Catalogue says.
func TestListCatalogueInstallationsOmitsRevoked(t *testing.T) {
	orgID, _, _, installation, _ := installFixture(t, "doc", "read")
	require.Contains(t, catalogueInstallations(t), installation.Id)

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, _, err := testStore.UninstallSolution(ctx, orgID, installation.Id)
		return err
	}))
	require.NotContains(t, catalogueInstallations(t), installation.Id)
}

// A grant above the root reaches inside the installation too, and is listed as
// inherited, never as granted here. An installation root is a single-label path
// and every write path requires a registered, non-empty node, so the only path
// above a root is the empty one and no write path produces it today: this seeds
// it directly, to hold the classification for the day scope trees deepen.
func TestListCatalogueInstallationsSeparatesInheritedTeams(t *testing.T) {
	orgID, _, roleID, installation, rootPath := installFixture(t, "doc", "read")
	granted := seedCatalogueTeam(t, orgID, "Granted Team")
	grantCatalogueTeam(t, orgID, granted, roleID, rootPath, nil)
	inherited := seedCatalogueTeam(t, orgID, "Org-wide Team")
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, err := storetx.Tx(ctx).Exec(ctx,
			`INSERT INTO scope_grants (id, org_id, subject_id, subject_kind, scope_path, role_id, created_at)
			 VALUES ($1, $2, $3, 'team', ''::ltree, $4, now())`,
			business.NewIDString(), orgID, inherited, roleID)
		return err
	}))

	record := catalogueInstallations(t)[installation.Id]
	require.NotNil(t, record)
	require.Len(t, record.GrantedTeams, 1)
	require.Equal(t, granted, record.GrantedTeams[0].GetGrant().GetSubjectId())
	require.Len(t, record.InheritedTeams, 1)
	require.Equal(t, inherited, record.InheritedTeams[0].GetGrant().GetSubjectId())
	require.Equal(t, "Org-wide Team", record.InheritedTeams[0].GetSubjectLabel())
}
