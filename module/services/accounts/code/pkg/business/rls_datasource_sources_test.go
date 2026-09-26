//go:build !pure

package business_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func insertDatasourceSource(t *testing.T, ctx context.Context, orgID, repo string) *business.DatasourceSource {
	t.Helper()
	nodeID := business.NewIDString()
	source := &business.DatasourceSource{
		ID:                  business.NewIDString(),
		OrgID:               orgID,
		Provider:            business.DatasourceProviderGitHub,
		Repo:                repo,
		Paths:               []string{"docs"},
		Branch:              "main",
		BoundaryNodeID:      nodeID,
		CredentialSecretRef: "cfs1:vault-transit:token-" + orgID,
		WebhookSecretRef:    "cfs1:vault-transit:hook-" + orgID,
		Status:              business.DatasourceStatusActive,
	}
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		if err := testStore.RegisterScopeNode(ctx, &gen.ScopeNode{
			Id:        nodeID,
			OrgId:     orgID,
			Kind:      business.ScopeNodeKindCollection,
			Label:     "guides",
			ScopePath: strings.ReplaceAll(nodeID, "-", "_"),
		}); err != nil {
			return err
		}
		return testStore.InsertDatasourceSource(ctx, source)
	}))
	return source
}

// TestRLS_DatasourceSources_CrossTenantBlocked mirrors the direct-org_id RLS
// tests: each org sees only its own datasource rows, a cross-tenant read is
// hidden even when the id is known, and an un-wrapped read (no app.current_org_id)
// returns nothing — RLS fail-closed. The unauthenticated webhook by-id lookup,
// which runs through the control-plane role, still resolves the row.
func TestRLS_DatasourceSources_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "alice-ds@rls-test.com", "alice-ds-rls", "Acme DS A")
	_, orgB := mustUserAndOrg(t, ctx, "bob-ds@rls-test.com", "bob-ds-rls", "Acme DS B")

	sourceA := insertDatasourceSource(t, ctx, orgA, "acme/a-docs")
	insertDatasourceSource(t, ctx, orgB, "acme/b-docs")

	// Own-org read sees exactly its own row.
	listA, err := testService.ListDatasourceSources(ctx, orgA)
	require.NoError(t, err)
	require.Len(t, listA, 1)
	require.Equal(t, "acme/a-docs", listA[0].Repo)

	got, err := testService.GetDatasourceSource(ctx, orgA, sourceA.ID)
	require.NoError(t, err)
	require.Equal(t, sourceA.ID, got.ID)

	// Cross-tenant read of a known id is hidden by RLS: the service reports
	// not-found rather than another org's row.
	_, err = testService.GetDatasourceSource(ctx, orgB, sourceA.ID)
	require.ErrorIs(t, err, business.ErrDatasourceSourceNotFound)

	listB, err := testService.ListDatasourceSources(ctx, orgB)
	require.NoError(t, err)
	require.Len(t, listB, 1)
	require.Equal(t, "acme/b-docs", listB[0].Repo)

	// Un-wrapped read (no org transaction) fails closed to zero rows.
	bare, err := testStore.ListDatasourceSources(ctx, orgA)
	require.NoError(t, err)
	require.Empty(t, bare)

	// The unauthenticated webhook path resolves the row via the control plane.
	viaControlPlane, err := testStore.GetDatasourceSourceByID(ctx, sourceA.ID)
	require.NoError(t, err)
	require.NotNil(t, viaControlPlane)
	require.Equal(t, orgA, viaControlPlane.OrgID)
	require.Equal(t, "cfs1:vault-transit:hook-"+orgA, viaControlPlane.WebhookSecretRef)
}

// TestDatasourceSource_DeleteRemovesRow confirms the org-scoped delete grant
// works and the row (and its stored credential envelopes) is gone afterward.
func TestDatasourceSource_DeleteRemovesRow(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, org := mustUserAndOrg(t, ctx, "del-ds@rls-test.com", "del-ds-rls", "Acme DS Del")
	source := insertDatasourceSource(t, ctx, org, "acme/del-docs")

	// The deleting statement returns the removed row's identity under the tenant
	// policy, and a second delete of the same id removes nothing and returns
	// nothing — the store contract the audit record depends on.
	require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
		removed, err := testStore.DeleteDatasourceSource(ctx, org, source.ID)
		if err != nil {
			return err
		}
		require.NotNil(t, removed)
		require.Equal(t, business.DatasourceProviderGitHub, removed.Provider)
		require.Equal(t, "acme/del-docs", removed.Repo)
		// Equality, not merely non-empty: it is what catches a transposed column
		// in the statement's RETURNING list.
		require.Equal(t, source.BoundaryNodeID, removed.BoundaryNodeID)

		again, err := testStore.DeleteDatasourceSource(ctx, org, source.ID)
		if err != nil {
			return err
		}
		require.Nil(t, again, "deleting a source that is gone removes nothing")
		return nil
	}))

	_, err := testService.GetDatasourceSource(ctx, org, source.ID)
	require.ErrorIs(t, err, business.ErrDatasourceSourceNotFound)
}

// TestGetOrCreateCollectionNode_ReusesByLabelPerOrg exercises the real store
// resolution: within one tenant a collection label maps to a single node
// (so multiple sources share one grantable boundary), a distinct label is a
// distinct node, and the same label in another tenant is a separate node — RLS
// keeps the lookup tenant-local.
func TestGetOrCreateCollectionNode_ReusesByLabelPerOrg(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "coll-a@rls-test.com", "coll-a-rls", "Coll A")
	_, orgB := mustUserAndOrg(t, ctx, "coll-b@rls-test.com", "coll-b-rls", "Coll B")

	create := func(org, label string) string {
		t.Helper()
		var id string
		require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
			nodeID := business.NewIDString()
			out, err := testStore.GetOrCreateCollectionNode(ctx, &gen.ScopeNode{
				Id: nodeID, OrgId: org, Kind: business.ScopeNodeKindCollection, Label: label,
				ScopePath: strings.ReplaceAll(nodeID, "-", "_"),
			})
			id = out
			return err
		}))
		return id
	}

	a1 := create(orgA, "guides")
	a2 := create(orgA, "guides")
	require.Equal(t, a1, a2, "the same label in one org must reuse the node")

	aDocs := create(orgA, "docs")
	require.NotEqual(t, a1, aDocs, "a different label must be a different node")

	b1 := create(orgB, "guides")
	require.NotEqual(t, a1, b1, "the same label in another org must be a distinct, tenant-isolated node")
}
