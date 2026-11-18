//go:build !pure

package infra_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/storetest"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/infra/storetx"
)

// The authority core's store over accounts' Postgres meets the contract every
// host's store meets (codefly-dev/authority storetest), against a real
// database: one transaction is all or nothing, every record reads back as
// written, revisions only grow, and identity is read from accounts' own
// relations: memberships, teams, and the organisation role a member
// administers by.
func TestTheAuthorityCoreStoreMeetsTheStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T, id storetest.Identity) authority.Store {
		seedAuthorityIdentity(t, id)
		return testStore.AuthorityCore()
	})
}

// seedAuthorityIdentity writes id into accounts' relations as accounts'
// own paths would: an organization with its owner, members with their
// membership role, and teams.
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
		}
		return nil
	}))
}

// An organisation role decides administration only. The core reads the name a
// member administers by ("admin", "owner") and nothing else: a role accounts'
// RBAC assigns a member, directly or through a team, is this host's own and is
// never handed to the core as something a person holds at a module.
func TestTheAuthorityCoreStoreHandsTheCoreNoOrganisationRoleButAdministration(t *testing.T) {
	org, admin, member, team := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	seedAuthorityIdentity(t, storetest.Identity{
		Organizations: []domain.Organization{{ID: org, Name: "RBAC " + org[:8]}},
		Members: []storetest.Member{
			{Organization: org, Person: admin, Administrator: true},
			{Organization: org, Person: member, Teams: []string{team}},
		},
	})
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		q := storetx.Tx(ctx)
		for subject, kind := range map[string]string{team: "team", member: "principal", admin: "principal"} {
			roleID := uuid.NewString()
			if _, err := q.Exec(ctx, `INSERT INTO roles (id, org_id, name, description) VALUES ($1, $2, $3, '')`, roleID, org, "reader "+roleID[:8]); err != nil {
				return err
			}
			if _, err := q.Exec(ctx, `INSERT INTO role_permissions (role_id, resource, action) VALUES ($1, 'rbac.item', 'read')`, roleID); err != nil {
				return err
			}
			if _, err := q.Exec(ctx, `INSERT INTO role_assignments (subject_id, subject_kind, role_id, org_id) VALUES ($1, $2, $3, $4)`, subject, kind, roleID, org); err != nil {
				return err
			}
		}
		return nil
	}))
	require.NoError(t, testStore.AuthorityCore().Read(testCtx, func(tx authority.Tx) error {
		a, err := tx.Member(org, admin)
		require.NoError(t, err)
		require.Equal(t, []string{"admin"}, a.Roles, "an administrator is named as one and holds no other organisation role at the core")
		require.True(t, decide.IsAdministrator(a))

		m, err := tx.Member(org, member)
		require.NoError(t, err)
		require.Equal(t, []string{team}, m.Teams)
		require.Empty(t, m.Roles, "a role assigned to a member or their team is no organisation role the core reads")
		require.False(t, decide.IsAdministrator(m))
		return nil
	}))
}

// The reach a consent approved reads back from accounts' store exactly as
// the core's encoder wrote it (decide.EncodeReach): the text, so its digest,
// and the rows it decodes to, operations and their policies included. The
// core compares an approved reach with the composition's by that text.
func TestAnApprovedReachReadsBackAsTheCoresEncoderWroteIt(t *testing.T) {
	org, admin := uuid.NewString(), uuid.NewString()
	seedAuthorityIdentity(t, storetest.Identity{
		Organizations: []domain.Organization{{ID: org, Name: "Reach " + org[:8]}},
		Members:       []storetest.Member{{Organization: org, Person: admin, Administrator: true}},
	})
	item := func(actions ...string) []*basev0.WorkScopeV1 {
		return []*basev0.WorkScopeV1{{ResourceKind: "reach-store.item", Actions: actions}}
	}
	page := []*basev0.WorkScopeV1{{ResourceKind: "reach-solution.page", Actions: []string{"read"}}}
	store := domain.Module{Name: "reach-store", Principal: "reach-store", Kind: domain.KindModule, ResourceKinds: item("admin", "read", "write"),
		Methods: map[string]domain.MethodPolicy{"/reach.Store/Get": {Requires: item("read")}, "POST /reach.Store/Get": {Requires: item("read")},
			"/reach.Store/Purge": {Requires: item("admin")}}}
	solution := domain.Module{Name: "reach-solution", Principal: "reach-solution", Kind: domain.KindSolution, ResourceKinds: page,
		Roles:    map[string][]*basev0.WorkScopeV1{domain.AdminRole: append(slices.Clone(page), item("read", "write")...)},
		Bindings: map[string]domain.Binding{"store": {ID: "store", Audience: "reach-store", InvokeScopes: item("read", "write")}}}
	reach, err := decide.ReachOf(solution, []domain.Module{solution, store})
	require.NoError(t, err)
	text, digest, err := decide.EncodeReach(reach)
	require.NoError(t, err)
	require.Contains(t, text, "/reach.Store/Get", "the reach lists the operation it admits")

	inst := domain.Installation{ID: uuid.NewString(), Organization: org, Target: solution.Name, Status: domain.Active, Revision: 1,
		AgentRole: domain.AdminRole, PrincipalID: uuid.NewString(), InstalledBy: admin,
		Consent: domain.ConsentReceipt{OperationID: uuid.NewString(), Intent: "intent", Revision: 1, Generation: 1, Digest: "sha256:offer", ReachDigest: digest, Reach: text}}
	require.NoError(t, testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error { return tx.PutInstallation(inst) }))
	var read domain.Installation
	require.NoError(t, testStore.AuthorityCore().Read(testCtx, func(tx authority.Tx) error {
		read, err = tx.Installation(inst.ID)
		return err
	}))
	require.Equal(t, text, read.Consent.Reach, "the approved reach is the encoder's text, byte for byte")
	require.Equal(t, digest, read.Consent.ReachDigest)
	decoded, err := decide.DecodeReach(read.Consent.Reach)
	require.NoError(t, err)
	again, againDigest, err := decide.EncodeReach(decoded)
	require.NoError(t, err)
	require.Equal(t, text, again, "what is read decodes to the rows the encoder wrote")
	require.Equal(t, digest, againDigest)
}

// A consent re-recorded with no reviewed plan is accepted only as the core's
// own adoption or re-convergence moves it: the same operation, intent, status,
// organization, target, agent role and principal, a generation that never
// goes back and a reach asking nothing new. Anything else is a new consent,
// refused without its plan (INSTALLATION_PLAN_REQUIRED), as a first record
// without a plan is.
func TestAConsentReRecordedWithoutAPlanMovesOnlyWhatTheCoreMoves(t *testing.T) {
	org, admin := uuid.NewString(), uuid.NewString()
	seedAuthorityIdentity(t, storetest.Identity{
		Organizations: []domain.Organization{{ID: org, Name: "Rerecord " + org[:8]}},
		Members:       []storetest.Member{{Organization: org, Person: admin, Administrator: true}},
	})
	item := []*basev0.WorkScopeV1{{ResourceKind: "rerecord.item", Actions: []string{"read"}}}
	reach := func(operations ...string) string {
		text, _, err := decide.EncodeReach(map[string][]domain.ReachRow{domain.UserRole: {{Path: []string{"rerecord", "store"}, Module: "store", Via: "binding", Scopes: item, Operations: operations}}})
		require.NoError(t, err)
		return text
	}
	approved := reach("/rerecord.Store/Get")
	base := domain.Installation{ID: uuid.NewString(), Organization: org, Target: "rerecord-" + org[:8], Status: domain.Active, Revision: 1,
		AgentRole: domain.AdminRole, PrincipalID: uuid.NewString(), InstalledBy: admin,
		Consent: domain.ConsentReceipt{OperationID: uuid.NewString(), Intent: "intent", Revision: 1, Generation: 2, Digest: "sha256:offer",
			ReachDigest: decide.DigestReach(approved), Reach: approved}}
	host := testStore.AuthorityCoreWithConsent(nil)
	put := func(inst domain.Installation) error {
		return host.Write(testCtx, func(tx authority.Tx) error { return tx.PutInstallation(inst) })
	}
	require.ErrorContains(t, put(base), "INSTALLATION_PLAN_REQUIRED", "a first record needs its reviewed plan")
	// A record the host kept no plan for (written before plans were kept).
	require.NoError(t, testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error { return tx.PutInstallation(base) }))

	unchanged := base
	unchanged.Revision = 2
	require.NoError(t, put(unchanged), "the core re-records the consent it holds")
	narrowed := unchanged
	narrowed.Consent.Generation, narrowed.Consent.Reach = 3, reach()
	narrowed.Consent.ReachDigest = decide.DigestReach(narrowed.Consent.Reach)
	require.NoError(t, put(narrowed), "a re-convergence that narrows the reach")

	for name, edit := range map[string]func(*domain.Installation){
		"another operation":       func(i *domain.Installation) { i.Consent.OperationID = uuid.NewString() },
		"another agent role":      func(i *domain.Installation) { i.AgentRole = domain.UserRole },
		"another principal":       func(i *domain.Installation) { i.PrincipalID = uuid.NewString() },
		"another target":          func(i *domain.Installation) { i.Target = "elsewhere" },
		"an earlier generation":   func(i *domain.Installation) { i.Consent.Generation = 1 },
		"a status moved":          func(i *domain.Installation) { i.Status = domain.Pending },
		"a reach asking for more": func(i *domain.Installation) { i.Consent.Reach = reach("/rerecord.Store/Purge") },
	} {
		changed := narrowed
		edit(&changed)
		require.ErrorContains(t, put(changed), "INSTALLATION_PLAN", "%s is a new consent, refused without its plan", name)
	}
}
