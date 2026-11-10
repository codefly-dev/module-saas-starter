//go:build !pure

package business_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"
)

// memberHolds counts what one person still holds in one organization in their
// own name, by the same names EndMemberAuthority reports them.
func memberHolds(t *testing.T, orgID, userID string) map[string]int {
	t.Helper()
	held := map[string]int{}
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		q := storetx.Tx(ctx)
		for name, query := range map[string]string{
			"role_assignments": `SELECT count(*) FROM role_assignments WHERE org_id = $1 AND subject_kind = 'principal' AND subject_id = $2`,
			"scope_grants":     `SELECT count(*) FROM scope_grants WHERE org_id = $1 AND subject_kind = 'principal' AND subject_id = $2`,
			"record_shares":    `SELECT count(*) FROM record_shares WHERE org_id = $1 AND subject_kind = 'principal' AND subject_id = $2`,
			"api_keys":         `SELECT count(*) FROM api_keys WHERE organization_id = $1 AND user_id = $2 AND revoked_at IS NULL`,
			"role_grants": `SELECT count(*) FROM authority_core_role_grants g JOIN authority_core_installations i ON i.id = g.installation_id
				WHERE i.organization_id = $1 AND g.person_id = $2::text`,
			"delegations": `SELECT count(*) FROM authority_core_delegations WHERE organization_id = $1 AND grantor = $2::text AND NOT revoked`,
		} {
			var n int
			if err := q.QueryRow(ctx, query, orgID, userID).Scan(&n); err != nil {
				return err
			}
			held[name] = n
		}
		return nil
	}))
	return held
}

// seedMemberAuthority gives one member every kind of grant in their own name
// that EndMemberAuthority ends, and one of each to a bystander in the same
// organization, who must keep theirs.
func seedMemberAuthority(t *testing.T, ctx context.Context, owner, orgID, userID string) {
	t.Helper()
	role := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{Id: role, Name: "reader " + role, OrgId: orgID,
			Permissions: []*gen.Permission{{Resource: "doc", Action: "read"}}})
	}))
	space := "s_" + strings.ReplaceAll(userID, "-", "_")
	_, err := testService.RegisterScopeNode(ctx, owner, &gen.RegisterScopeNodeRequest{OrgId: orgID, ScopePath: space, Kind: "space", Label: "Space"})
	require.NoError(t, err)
	_, err = testService.AssignRole(ctx, &gen.AssignRoleRequest{SubjectId: userID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: role, OrgId: orgID})
	require.NoError(t, err)
	_, err = testService.GrantScope(ctx, owner, &gen.GrantScopeRequest{OrgId: orgID, SubjectId: userID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: space, RoleId: role})
	require.NoError(t, err)
	_, err = testService.ShareRecord(ctx, owner, &gen.ShareRecordRequest{OrgId: orgID, ResourceType: "doc", ResourceId: "shared-" + userID,
		SubjectId: userID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: role})
	require.NoError(t, err)
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return testStore.CreateAPIKey(ctx, &gen.APIKey{Id: business.NewIDString(), OrganizationId: orgID, UserId: userID, Name: "k", Prefix: "cfly_sk_live_"},
			"hash-"+business.NewIDString())
	}))
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		q := storetx.Tx(ctx)
		installation := uuid.NewString()
		if _, err := q.Exec(ctx, `INSERT INTO authority_core_installations (id, organization_id, target, status, revision) VALUES ($1, $2, 'wiki', 'active', 1)
			ON CONFLICT (id) DO NOTHING`, installation, orgID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `INSERT INTO authority_core_role_grants (installation_id, role, person_id) VALUES ($1, 'admin', $2)`, installation, userID); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `INSERT INTO authority_core_delegations (id, organization_id, grantor, module, scopes) VALUES ($1, $2, $3, 'org-directory', '[]'::jsonb)`,
			uuid.NewString(), orgID, userID)
		return err
	}))
}

var everyGrantKind = map[string]int{"role_assignments": 1, "scope_grants": 1, "record_shares": 1, "api_keys": 1, "role_grants": 1, "delegations": 1}

// A removed member's grants end with the membership, and a person added again
// starts with nothing: the role assignments, scope grants, shares, installation
// roles, delegations and API keys they held in their own name are not
// "dormant until the next add". What ended is named on the audit event.
// Before this test's change none of them was touched by the removal.
func TestRemoveOrgMemberEndsWhatTheMemberHeldInTheirOwnName(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, orgID := mustUserAndOrg(t, ctx, "owner@ends-test.com", "ends-owner", "Ends Org")
	member, _ := mustUserAndOrg(t, ctx, "member@ends-test.com", "ends-member", "Ends Member Org")
	bystander, _ := mustUserAndOrg(t, ctx, "bystander@ends-test.com", "ends-bystander", "Ends Bystander Org")
	for _, id := range []string{member, bystander} {
		require.NoError(t, testService.AddOrgMember(ctx, owner, &gen.AddOrgMemberRequest{OrgId: orgID, UserId: id, Role: gen.OrgRole_ORG_ROLE_MEMBER}))
	}
	seedMemberAuthority(t, ctx, owner, orgID, member)
	seedMemberAuthority(t, ctx, owner, orgID, bystander)
	require.Equal(t, everyGrantKind, memberHolds(t, orgID, member))

	require.NoError(t, testService.RemoveOrgMember(ctx, owner, &gen.RemoveOrgMemberRequest{OrgId: orgID, UserId: member}))
	require.Equal(t, map[string]int{"role_assignments": 0, "scope_grants": 0, "record_shares": 0, "api_keys": 0, "role_grants": 0, "delegations": 0},
		memberHolds(t, orgID, member), "nothing is left to come back")
	require.Equal(t, everyGrantKind, memberHolds(t, orgID, bystander), "another member's grants are untouched")

	require.NoError(t, testService.AddOrgMember(ctx, owner, &gen.AddOrgMemberRequest{OrgId: orgID, UserId: member, Role: gen.OrgRole_ORG_ROLE_MEMBER}))
	require.Equal(t, map[string]int{"role_assignments": 0, "scope_grants": 0, "record_shares": 0, "api_keys": 0, "role_grants": 0, "delegations": 0},
		memberHolds(t, orgID, member), "a person added again holds nothing they held before")

	events, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{OrgID: orgID, EventType: string(business.EventOrgMemberRemoved)})
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.ElementsMatch(t, []any{"api_keys:1", "delegations:1", "record_shares:1", "role_assignments:1", "role_grants:1", "scope_grants:1"},
		events[0].Payload["ended_authority"], "the event says what ended")
}

// Leaving is the same narrowing as being removed.
func TestLeavingAnOrganizationEndsWhatTheMemberHeldInTheirOwnName(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, orgID := mustUserAndOrg(t, ctx, "owner@leaves-test.com", "leaves-owner", "Leaves Org")
	member, _ := mustUserAndOrg(t, ctx, "member@leaves-test.com", "leaves-member", "Leaves Member Org")
	require.NoError(t, testService.AddOrgMember(ctx, owner, &gen.AddOrgMemberRequest{OrgId: orgID, UserId: member, Role: gen.OrgRole_ORG_ROLE_MEMBER}))
	seedMemberAuthority(t, ctx, owner, orgID, member)

	require.NoError(t, testService.LeaveOrganization(ctx, member, &gen.LeaveOrganizationRequest{OrgId: orgID}))
	require.Equal(t, map[string]int{"role_assignments": 0, "scope_grants": 0, "record_shares": 0, "api_keys": 0, "role_grants": 0, "delegations": 0},
		memberHolds(t, orgID, member))
}
