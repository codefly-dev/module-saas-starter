package business_test

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// listScopePaths returns the set of scope paths ListAccessibleScopes reports for
// a subject acting on (resourceType, action).
func listScopePaths(t *testing.T, ctx context.Context, orgID, subjectID string, kind gen.SubjectKind, resourceType, action string) map[string]bool {
	t.Helper()
	resp, err := testService.ListAccessibleScopes(ctx, &gen.ListAccessibleScopesRequest{
		OrgId: orgID, SubjectId: subjectID, SubjectKind: kind, ResourceType: resourceType, Action: action,
	})
	require.NoError(t, err)
	set := map[string]bool{}
	for _, s := range resp.GetScopes() {
		set[s.GetScopePath()] = true
	}
	return set
}

// TestListAccessibleScopes_GrantRevokeAndShare proves the acceptance behaviors:
// a grant makes a boundary (and its whole subtree) appear for the granted
// subject and disappear on revoke; a team grant is inherited by a member; and a
// per-record share surfaces the placed-record node.
func TestListAccessibleScopes_GrantRevokeAndShare(t *testing.T) {
	clearData(t)
	ctx := testCtx

	owner, org := mustUserAndOrg(t, ctx, "las-owner@rls-test.com", "las-owner", "LAS Org")

	roleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id: roleID, Name: "viewer " + roleID, OrgId: org,
			Permissions: []*gen.Permission{{Resource: "doc", Action: "read"}},
		})
	}))

	// A solution node, a collection under it, and a placed record in the collection.
	register := func(path, kind, label, rtype, rid string) {
		_, err := testService.RegisterScopeNode(ctx, owner, &gen.RegisterScopeNodeRequest{
			OrgId: org, ScopePath: path, Kind: kind, Label: label, ResourceType: rtype, ResourceId: rid,
		})
		require.NoError(t, err)
	}
	register("sol", business.ScopeNodeKindSolution, "Solution", "", "")
	register("sol.col", business.ScopeNodeKindCollection, "Collection", "", "")
	register("sol.col.doc_1", "record", "Doc 1", "doc", "doc-1")

	// No grant yet: nothing visible.
	require.Empty(t, listScopePaths(t, ctx, org, owner, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "doc", "read"))

	// Grant at the collection: the collection and its subtree (the placed record)
	// appear; the ancestor solution node does not (the grant is not above it).
	_, err := testService.GrantScope(ctx, owner, &gen.GrantScopeRequest{
		OrgId: org, SubjectId: owner, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		ScopePath: "sol.col", RoleId: roleID,
	})
	require.NoError(t, err)

	granted := listScopePaths(t, ctx, org, owner, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "doc", "read")
	require.True(t, granted["sol.col"], "the granted collection must be listed")
	require.True(t, granted["sol.col.doc_1"], "the subtree record must be listed")
	require.False(t, granted["sol"], "the ancestor solution node is not below the grant")

	// Revoke in the same shape: the boundary disappears again.
	require.NoError(t, testService.RevokeScope(ctx, owner, &gen.RevokeScopeRequest{
		OrgId: org, SubjectId: owner, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		ScopePath: "sol.col", RoleId: roleID,
	}))
	require.Empty(t, listScopePaths(t, ctx, org, owner, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "doc", "read"),
		"revoked grant must remove the boundary")

	// A team grant is inherited by a member.
	team, err := testService.CreateTeam(ctx, owner, &gen.CreateTeamRequest{OrgId: org, Name: "team-las"})
	require.NoError(t, err)
	require.NoError(t, testService.AddTeamMember(ctx, owner, &gen.AddTeamMemberRequest{
		TeamId: team.Team.Id, UserId: owner, Role: gen.TeamRole_TEAM_ROLE_OWNER,
	}))
	_, err = testService.GrantScope(ctx, owner, &gen.GrantScopeRequest{
		OrgId: org, SubjectId: team.Team.Id, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM,
		ScopePath: "sol", RoleId: roleID,
	})
	require.NoError(t, err)
	viaTeam := listScopePaths(t, ctx, org, owner, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "doc", "read")
	require.True(t, viaTeam["sol"] && viaTeam["sol.col"] && viaTeam["sol.col.doc_1"],
		"a member inherits the team grant across the whole subtree: %v", viaTeam)

	// A per-record share surfaces exactly the placed-record node for a subject with
	// no scope grant of their own (record_shares.subject_id has no principal FK, so
	// a bare principal id is a valid share recipient).
	other := business.NewIDString()
	require.Empty(t, listScopePaths(t, ctx, org, other, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "doc", "read"))
	_, err = testService.ShareRecord(ctx, owner, &gen.ShareRecordRequest{
		OrgId: org, ResourceType: "doc", ResourceId: "doc-1",
		SubjectId: other, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: roleID,
	})
	require.NoError(t, err)
	shared := listScopePaths(t, ctx, org, other, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "doc", "read")
	require.Equal(t, map[string]bool{"sol.col.doc_1": true}, shared,
		"a share surfaces only the shared record's node")
}

// TestListAccessibleScopes_Paginates proves the result is bounded and resumable:
// a broad grant reaches many nodes, but a small page_size returns them in
// scope_path-ordered pages that together cover every node exactly once, with an
// empty next_page_token only on the final page.
func TestListAccessibleScopes_Paginates(t *testing.T) {
	clearData(t)
	ctx := testCtx

	owner, org := mustUserAndOrg(t, ctx, "page-owner@rls-test.com", "page-owner", "Page Org")
	roleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id: roleID, Name: "viewer " + roleID, OrgId: org,
			Permissions: []*gen.Permission{{Resource: "doc", Action: "read"}},
		})
	}))

	// A root plus several children; a grant at the root reaches the whole subtree.
	_, err := testService.RegisterScopeNode(ctx, owner, &gen.RegisterScopeNodeRequest{
		OrgId: org, ScopePath: "root", Kind: "space", Label: "Root",
	})
	require.NoError(t, err)
	want := map[string]bool{"root": true}
	for i := 0; i < 6; i++ {
		p := fmt.Sprintf("root.n%d", i)
		_, err := testService.RegisterScopeNode(ctx, owner, &gen.RegisterScopeNodeRequest{
			OrgId: org, ScopePath: p, Kind: "node", Label: p,
		})
		require.NoError(t, err)
		want[p] = true
	}
	_, err = testService.GrantScope(ctx, owner, &gen.GrantScopeRequest{
		OrgId: org, SubjectId: owner, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		ScopePath: "root", RoleId: roleID,
	})
	require.NoError(t, err)

	got := map[string]bool{}
	token := ""
	pages := 0
	for {
		resp, err := testService.ListAccessibleScopes(ctx, &gen.ListAccessibleScopesRequest{
			OrgId: org, SubjectId: owner, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			ResourceType: "doc", Action: "read", PageSize: 2, PageToken: token,
		})
		require.NoError(t, err)
		require.LessOrEqual(t, len(resp.GetScopes()), 2, "a page must not exceed page_size")
		for _, s := range resp.GetScopes() {
			require.False(t, got[s.GetScopePath()], "node %s returned on more than one page", s.GetScopePath())
			got[s.GetScopePath()] = true
		}
		pages++
		token = resp.GetNextPageToken()
		if token == "" {
			break
		}
		require.Less(t, pages, 100, "pagination did not terminate")
	}
	require.Equal(t, want, got, "the pages together must cover every accessible node exactly once")
	require.GreaterOrEqual(t, pages, 4, "7 nodes at page_size 2 must span multiple pages")
}

// TestListAccessibleScopes_ListedNodesAuthorizeRecords is the round-trip property
// the issue asked for (#481 ask 3): over random trees, grants, and shares, every
// node the list returns authorizes a record placed at it under CheckAccess, and
// every placed record CheckAccess allows has its node in the list. A structural
// node carries no resource of its own, so it is probed with a record placed
// directly beneath it (a listed structural node means a grant at-or-above it,
// which must reach that child); a record node is its own placed record.
func TestListAccessibleScopes_ListedNodesAuthorizeRecords(t *testing.T) {
	rng := rand.New(rand.NewSource(0x5C0DE))
	resources := []string{"doc", "*", "other"}
	actions := []string{"read", "*", "write"}

	for iter := 0; iter < 12; iter++ {
		clearData(t)
		ctx := testCtx
		owner, org := mustUserAndOrg(t, ctx,
			fmt.Sprintf("round-%d@rls-test.com", iter), fmt.Sprintf("round-%d", iter), "Round Org")

		var roles []string
		for r := 0; r < 1+rng.Intn(3); r++ {
			rid := business.NewIDString()
			perm := &gen.Permission{Resource: resources[rng.Intn(len(resources))], Action: actions[rng.Intn(len(actions))]}
			require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
				return testStore.CreateRole(ctx, &gen.Role{
					Id: rid, Name: "role " + rid, OrgId: org, Permissions: []*gen.Permission{perm},
				})
			}))
			roles = append(roles, rid)
		}

		register := func(path, rtype, rid string) {
			_, err := testService.RegisterScopeNode(ctx, owner, &gen.RegisterScopeNodeRequest{
				OrgId: org, ScopePath: path, Kind: "node", Label: path, ResourceType: rtype, ResourceId: rid,
			})
			require.NoError(t, err)
		}

		// Structural tree: a root and a few children, each carrying exactly one
		// placed doc record directly beneath it. recByNode maps a structural node to
		// the record probing it; recByPath maps a record's own node path to it.
		type rec struct{ path, id string }
		recByNode := map[string]rec{}
		recByPath := map[string]rec{}
		var records []rec
		var structural []string
		recID := 0
		addStructural := func(node string) {
			structural = append(structural, node)
			r := rec{path: node + ".rec", id: fmt.Sprintf("doc-%d", recID)}
			recID++
			register(r.path, "doc", r.id)
			recByNode[node] = r
			recByPath[r.path] = r
			records = append(records, r)
		}
		register("root", "", "")
		addStructural("root")
		for c := 0; c < 1+rng.Intn(3); c++ {
			p := fmt.Sprintf("root.n%d", c)
			register(p, "", "")
			addStructural(p)
		}

		grantTargets := append([]string{}, structural...)
		for _, r := range records {
			grantTargets = append(grantTargets, r.path)
		}
		for g := 0; g < rng.Intn(len(grantTargets)+1); g++ {
			_, err := testService.GrantScope(ctx, owner, &gen.GrantScopeRequest{
				OrgId: org, SubjectId: owner, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
				ScopePath: grantTargets[rng.Intn(len(grantTargets))], RoleId: roles[rng.Intn(len(roles))],
			})
			require.NoError(t, err)
		}
		for s := 0; s < rng.Intn(len(records)+1); s++ {
			r := records[rng.Intn(len(records))]
			_, err := testService.ShareRecord(ctx, owner, &gen.ShareRecordRequest{
				OrgId: org, ResourceType: "doc", ResourceId: r.id,
				SubjectId: owner, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: roles[rng.Intn(len(roles))],
			})
			require.NoError(t, err)
		}

		checkAccess := func(id, action string) bool {
			var allowed bool
			require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
				var e error
				allowed, _, e = testStore.CheckAccess(ctx, owner, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "doc", id, action)
				return e
			}))
			return allowed
		}

		for _, action := range []string{"read", "write"} {
			listed := listScopePaths(t, ctx, org, owner, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "doc", action)

			// Forward: every listed node authorizes a record placed at it.
			for path := range listed {
				if r, ok := recByNode[path]; ok {
					require.Truef(t, checkAccess(r.id, action),
						"iter %d action %q: listed structural node %s must authorize a record placed beneath it",
						iter, action, path)
				} else if r, ok := recByPath[path]; ok {
					require.Truef(t, checkAccess(r.id, action),
						"iter %d action %q: listed record node %s must pass CheckAccess",
						iter, action, path)
				} else {
					t.Fatalf("iter %d: listed node %s is neither a known structural nor record node", iter, path)
				}
			}

			// Backward: every placed record CheckAccess allows has its node listed.
			for _, r := range records {
				if checkAccess(r.id, action) {
					require.Truef(t, listed[r.path],
						"iter %d action %q: CheckAccess allows record %s but its node is not listed",
						iter, action, r.path)
				}
			}
		}
	}
}

// TestListAccessibleScopes_AgreesWithCheckAccess is the never-disagree property:
// over random trees, roles, grants, and shares, a placed-record node is in
// ListAccessibleScopes exactly when CheckAccess allows the same (subject,
// resource_type, action) on that record.
func TestListAccessibleScopes_AgreesWithCheckAccess(t *testing.T) {
	rng := rand.New(rand.NewSource(0xB0574))
	resources := []string{"doc", "*", "other"}
	actions := []string{"read", "*", "write"}

	for iter := 0; iter < 12; iter++ {
		clearData(t)
		ctx := testCtx
		owner, org := mustUserAndOrg(t, ctx,
			fmt.Sprintf("prop-%d@rls-test.com", iter), fmt.Sprintf("prop-%d", iter), "Prop Org")

		// A handful of roles with random single-permission grants.
		var roles []string
		for r := 0; r < 1+rng.Intn(3); r++ {
			rid := business.NewIDString()
			perm := &gen.Permission{Resource: resources[rng.Intn(len(resources))], Action: actions[rng.Intn(len(actions))]}
			require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
				return testStore.CreateRole(ctx, &gen.Role{
					Id: rid, Name: "role " + rid, OrgId: org, Permissions: []*gen.Permission{perm},
				})
			}))
			roles = append(roles, rid)
		}

		register := func(path, rtype, rid string) {
			_, err := testService.RegisterScopeNode(ctx, owner, &gen.RegisterScopeNodeRequest{
				OrgId: org, ScopePath: path, Kind: "node", Label: path, ResourceType: rtype, ResourceId: rid,
			})
			require.NoError(t, err)
		}

		// Structural tree: a root and a few children.
		register("root", "", "")
		structural := []string{"root"}
		for c := 0; c < 1+rng.Intn(3); c++ {
			p := fmt.Sprintf("root.n%d", c)
			register(p, "", "")
			structural = append(structural, p)
		}

		// Placed doc records under random structural parents.
		type rec struct{ path, id string }
		var records []rec
		for d := 0; d < 2+rng.Intn(4); d++ {
			parent := structural[rng.Intn(len(structural))]
			id := fmt.Sprintf("doc-%d", d)
			p := fmt.Sprintf("%s.doc_%d", parent, d)
			register(p, "doc", id)
			records = append(records, rec{p, id})
		}

		grantTargets := append([]string{}, structural...)
		for _, r := range records {
			grantTargets = append(grantTargets, r.path)
		}
		for g := 0; g < rng.Intn(len(grantTargets)+1); g++ {
			_, err := testService.GrantScope(ctx, owner, &gen.GrantScopeRequest{
				OrgId: org, SubjectId: owner, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
				ScopePath: grantTargets[rng.Intn(len(grantTargets))], RoleId: roles[rng.Intn(len(roles))],
			})
			require.NoError(t, err)
		}
		for s := 0; s < rng.Intn(len(records)+1); s++ {
			r := records[rng.Intn(len(records))]
			_, err := testService.ShareRecord(ctx, owner, &gen.ShareRecordRequest{
				OrgId: org, ResourceType: "doc", ResourceId: r.id,
				SubjectId: owner, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: roles[rng.Intn(len(roles))],
			})
			require.NoError(t, err)
		}

		// The two oracles must agree on every placed record, for concrete queries.
		for _, action := range []string{"read", "write"} {
			listed := listScopePaths(t, ctx, org, owner, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "doc", action)
			for _, r := range records {
				var allowed bool
				require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
					var e error
					allowed, _, e = testStore.CheckAccess(ctx, owner, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "doc", r.id, action)
					return e
				}))
				require.Equalf(t, allowed, listed[r.path],
					"iter %d action %q: disagreement on %s (CheckAccess=%v, listed=%v)",
					iter, action, r.path, allowed, listed[r.path])
			}
		}
	}
}
