//go:build !pure

package infra_test

import (
	"context"
	"strings"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/scope"
	"github.com/codefly-dev/authority/libraries/authority-core/go/storetest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/infra/storetx"
)

// The authority core's store over accounts' Postgres meets the contract every
// host's store meets (codefly-dev/authority storetest), against a real
// database: one transaction is all or nothing, every record reads back as
// written, revisions only grow, and identity is read from accounts' own
// relations: memberships, teams, and organization roles synthesized from role
// assignments with or without a resource scope.
func TestTheAuthorityCoreStoreMeetsTheStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T, id storetest.Identity) authority.Store {
		seedAuthorityIdentity(t, id)
		return testStore.AuthorityCore()
	})
}

// seedAuthorityIdentity writes id into accounts' relations as accounts'
// own paths would: an organization with its owner, members with their
// membership role, teams, and one role per member whose permissions are the
// member's scopes, assigned with the resource each names (or none).
func seedAuthorityIdentity(t *testing.T, id storetest.Identity) {
	t.Helper()
	owners := map[string]string{}
	for _, m := range id.Members {
		if _, ok := owners[m.Organization]; !ok {
			owners[m.Organization] = m.Person
		}
	}
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		q := storetx.Tx(ctx)
		people := map[string]bool{}
		for _, m := range id.Members {
			if people[m.Person] {
				continue
			}
			people[m.Person] = true
			if _, err := q.Exec(ctx, `INSERT INTO users (uuid, primary_email, status, profile) VALUES ($1, $2, 'active', '{}'::jsonb)`,
				m.Person, "storetest-"+m.Person+"@example.com"); err != nil {
				return err
			}
		}
		for _, o := range id.Organizations {
			slug := "storetest-" + strings.ToLower(strings.ReplaceAll(o.ID, "-", ""))[:20]
			if _, err := q.Exec(ctx, `INSERT INTO organizations (id, name, slug, owner_id) VALUES ($1, $2, $3, $4)`, o.ID, o.Name, slug, owners[o.ID]); err != nil {
				return err
			}
		}
		// The directory holds teams independently of membership: an entry of
		// id.Teams may have no members at all.
		teams := map[string]bool{}
		insertTeam := func(organization, team string) error {
			if teams[team] {
				return nil
			}
			teams[team] = true
			slug := "t" + strings.ReplaceAll(team, "-", "")[:12]
			_, err := q.Exec(ctx, `INSERT INTO teams (id, org_id, name, slug, path) VALUES ($1, $2, $3, $4, $4)`, team, organization, "Storetest "+team, slug)
			return err
		}
		for _, team := range id.Teams {
			if err := insertTeam(team.Organization, team.ID); err != nil {
				return err
			}
		}
		for _, m := range id.Members {
			role := "member"
			if m.Administrator {
				role = "admin"
			}
			if _, err := q.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, $3)`, m.Organization, m.Person, role); err != nil {
				return err
			}
			for _, team := range m.Teams {
				if err := insertTeam(m.Organization, team); err != nil {
					return err
				}
				if _, err := q.Exec(ctx, `INSERT INTO team_members (team_id, user_id, org_id) VALUES ($1, $2, $3)`, team, m.Person, m.Organization); err != nil {
					return err
				}
			}
			for _, sc := range m.Scopes {
				roleID := uuid.NewString()
				if _, err := q.Exec(ctx, `INSERT INTO roles (id, org_id, name, description) VALUES ($1, $2, $3, '')`, roleID, m.Organization, "storetest "+roleID[:8]); err != nil {
					return err
				}
				for _, action := range sc.Actions {
					if _, err := q.Exec(ctx, `INSERT INTO role_permissions (role_id, resource, action) VALUES ($1, $2, $3)`, roleID, sc.Kind, action); err != nil {
						return err
					}
				}
				var resource *string
				if len(sc.Resources) > 0 {
					resource = &sc.Resources[0].ID
				}
				if _, err := q.Exec(ctx, `INSERT INTO role_assignments (subject_id, subject_kind, role_id, org_id, scope) VALUES ($1, 'principal', $2, $3, $4)`,
					m.Person, roleID, m.Organization, resource); err != nil {
					return err
				}
			}
		}
		return nil
	}))
}

// An organization administrator holds every action on every kind an audience
// serves (accounts' RBAC: an admin or owner is allowed every permission), and
// a person's roles through a team are theirs while they are on it.
func TestTheAuthorityCoreStoreReadsAccountsRBAC(t *testing.T) {
	org, admin, member, team := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	seedAuthorityIdentity(t, storetest.Identity{
		Organizations: []domain.Organization{{ID: org, Name: "RBAC " + org[:8]}},
		Members: []storetest.Member{
			{Organization: org, Person: admin, Administrator: true},
			{Organization: org, Person: member, Teams: []string{team}},
		},
	})
	roleID := uuid.NewString()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		q := storetx.Tx(ctx)
		if _, err := q.Exec(ctx, `INSERT INTO roles (id, org_id, name, description) VALUES ($1, $2, 'team reader', '')`, roleID, org); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `INSERT INTO role_permissions (role_id, resource, action) VALUES ($1, 'rbac.item', 'read')`, roleID); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `INSERT INTO role_assignments (subject_id, subject_kind, role_id, org_id) VALUES ($1, 'team', $2, $3)`, team, roleID, org)
		return err
	}))
	require.NoError(t, testStore.AuthorityCore().Read(testCtx, func(tx authority.Tx) error {
		a, err := tx.Member(org, admin)
		require.NoError(t, err)
		require.Contains(t, a.Roles, "admin")
		role, err := tx.OrgRole(org, "admin")
		require.NoError(t, err)
		require.Equal(t, []domain.RoleScope{{Kind: scope.AnyKind, Actions: []string{scope.AnyAction}}}, role.Scopes)

		m, err := tx.Member(org, member)
		require.NoError(t, err)
		require.Equal(t, []string{team}, m.Teams)
		var held []domain.RoleScope
		for _, name := range m.Roles {
			r, err := tx.OrgRole(org, name)
			require.NoError(t, err)
			held = append(held, r.Scopes...)
		}
		require.Equal(t, []domain.RoleScope{{Kind: "rbac.item", Actions: []string{"read"}}}, held, "the team's role is the member's")
		return nil
	}))
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, err := storetx.Tx(ctx).Exec(ctx, `DELETE FROM team_members WHERE team_id = $1 AND user_id = $2`, team, member)
		return err
	}))
	require.NoError(t, testStore.AuthorityCore().Read(testCtx, func(tx authority.Tx) error {
		m, err := tx.Member(org, member)
		require.NoError(t, err)
		require.Empty(t, m.Teams)
		require.Empty(t, m.Roles, "a person off the team holds none of its roles")
		return nil
	}))
}
