package adapters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func TestAuditCrossOrganizationReadsAreDenied(t *testing.T) {
	const foreignOrg = "019f6bf7-9999-7aaa-8bbb-cccccccccccc"
	reads := []struct {
		name   string
		invoke func(context.Context) error
	}{
		{"members", func(ctx context.Context) error {
			_, err := (&OrgServer{}).ListMembers(ctx, &gen.ListOrgMembersRequest{OrgId: foreignOrg})
			return err
		}},
		{"teams", func(ctx context.Context) error {
			_, err := (&TeamServer{}).ListTeams(ctx, &gen.ListTeamsRequest{OrgId: foreignOrg})
			return err
		}},
		{"roles", func(ctx context.Context) error {
			_, err := (&PermServer{}).ListRoles(ctx, &gen.ListRolesRequest{OrgId: foreignOrg})
			return err
		}},
		{"api keys", func(ctx context.Context) error {
			_, err := (&APIKeyServer{}).ListAPIKeys(ctx, &gen.ListAPIKeysRequest{OrganizationId: foreignOrg, PageSize: 10})
			return err
		}},
	}
	for _, read := range reads {
		t.Run(read.name, func(t *testing.T) {
			installLayeredAuthzService(t, &layeredAuthzStore{role: gen.OrgRole_ORG_ROLE_ADMIN})
			ctx := stampVerifiedIdentity(context.Background(), layeredActorID, layeredOrgID, auth.Assurance{})
			require.Equal(t, codes.PermissionDenied, status.Code(read.invoke(ctx)))
		})
	}
}

func TestAuditTeamAdminCannotMutateOrganizationAuthority(t *testing.T) {
	writes := []struct {
		name   string
		invoke func(context.Context) error
	}{
		{"create role", func(ctx context.Context) error {
			_, err := (&PermServer{}).CreateRole(ctx, &gen.CreateRoleRequest{OrgId: teamAdminOrgID, Name: "Example role"})
			return err
		}},
		{"assign role", func(ctx context.Context) error {
			_, err := (&PermServer{}).AssignRole(ctx, &gen.AssignRoleRequest{OrgId: teamAdminOrgID, RoleId: platformTargetD, SubjectId: teamAdminActorID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL})
			return err
		}},
		{"create team", func(ctx context.Context) error {
			_, err := (&TeamServer{}).CreateTeam(ctx, &gen.CreateTeamRequest{OrgId: teamAdminOrgID, Name: "Example team"})
			return err
		}},
		{"create api key", func(ctx context.Context) error {
			_, err := (&APIKeyServer{}).CreateAPIKey(ctx, &gen.CreateAPIKeyRequest{OrganizationId: teamAdminOrgID, Name: "Example key"})
			return err
		}},
		{"revoke api key", func(ctx context.Context) error {
			_, err := (&APIKeyServer{}).RevokeAPIKey(ctx, &gen.RevokeAPIKeyRequest{OrganizationId: teamAdminOrgID, Id: platformTargetD})
			return err
		}},
	}
	for _, write := range writes {
		t.Run(write.name, func(t *testing.T) {
			store := &teamAdminStore{orgMember: true, orgRole: gen.OrgRole_ORG_ROLE_MEMBER, teamMember: true, teamRole: gen.TeamRole_TEAM_ROLE_ADMIN}
			installTeamAdminService(t, store)
			require.Equal(t, codes.PermissionDenied, status.Code(write.invoke(teamAdminContext())))
		})
	}
}
