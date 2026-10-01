//go:build !pure

package business_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	// The boundary's name is read with the row, under the same org scope, so a
	// member who lists sources sees the collection named, not its node id.
	require.Equal(t, "guides", listA[0].BoundaryLabel)

	got, err := testService.GetDatasourceSource(ctx, orgA, sourceA.ID)
	require.NoError(t, err)
	require.Equal(t, sourceA.ID, got.ID)
	require.Equal(t, "guides", got.BoundaryLabel)

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

// TestDatasourceSource_GitHubConnectLockSerializesConnects proves the lock the
// duplicate-source check takes (issue #978) is a real transaction-scoped lock:
// a second connect of the same repository waits for the first transaction to
// end, and the lock is refused outside a tenant transaction.
func TestDatasourceSource_GitHubConnectLockSerializesConnects(t *testing.T) {
	clearData(t)
	ctx := testCtx
	_, org := mustUserAndOrg(t, ctx, "carol-ds@rls-test.com", "carol-ds-rls", "Acme DS C")

	require.Error(t, testStore.LockDatasourceGitHubSourceConnect(ctx, org, "acme/docs"))

	held := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
			if err := testStore.LockDatasourceGitHubSourceConnect(ctx, org, "acme/docs"); err != nil {
				close(held)
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	secondLocked := make(chan error, 1)
	go func() {
		secondLocked <- testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
			// Case-insensitive, like the duplicate check.
			return testStore.LockDatasourceGitHubSourceConnect(ctx, org, "Acme/Docs")
		})
	}()
	select {
	case err := <-secondLocked:
		t.Fatalf("second connect took the lock while the first held it (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondLocked)
}

// TestAddGitHubSource_ConcurrentDuplicateConnectsLeaveOneSource proves the
// duplicate refusal (issue #978) against Postgres rather than the fake store,
// whose lock is a no-op: several connects of the same repository, branch and
// collection racing each other end with exactly one source, and every other
// connect is refused with AlreadyExists. It depends on the lock being taken
// before the existing-source read and on that read seeing the winner's commit,
// so it fails if either moves (for example a stricter isolation level whose
// snapshot predates the lock).
func TestAddGitHubSource_ConcurrentDuplicateConnectsLeaveOneSource(t *testing.T) {
	clearData(t)
	ctx := testCtx
	_, org := mustUserAndOrg(t, ctx, "dave-ds@rls-test.com", "dave-ds-rls", "Acme DS D")

	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	producer := &recordingProducer{}
	svc.SetDatasourceConnector(&countingCipher{}, producer, "")
	connectProducers.Store(svc, producer)
	svc.SetAuditEmitter(&recordingAudit{})
	gh := &fakeGitHub{defaultBranch: "main", commit: "abc", public: true}
	svc.SetDatasourceGitHubClientFactory(func(string) business.GitHubContentClient { return gh })

	// Connect into an existing collection by its node id. Connecting by label
	// would also take the collection-label lock, which serializes the racers
	// on its own and would hide a missing repository lock.
	seed, err := svc.AddGitHubSource(ctx, "actor-1", business.AddGitHubSourceInput{
		OrgID: org, Repo: "acme/seed", CollectionLabel: "handbook",
	})
	require.NoError(t, err)

	const racers = 8
	// Open one pooled connection per racer first. A cold pool hands the first
	// racer its idle connection while the rest wait tens of milliseconds for
	// new ones, by which time the first has committed, so the race this test
	// exists for would never be run.
	var warm sync.WaitGroup
	warm.Add(racers)
	warmErrs := make(chan error, racers)
	for range racers {
		go func() {
			warmErrs <- testStore.WithOrgTx(ctx, org, func(context.Context) error {
				warm.Done()
				warm.Wait()
				return nil
			})
		}()
	}
	for range racers {
		require.NoError(t, <-warmErrs)
	}

	start := make(chan struct{})
	errs := make(chan error, racers)
	for i := range racers {
		repo := "acme/docs"
		if i%2 == 1 {
			repo = "Acme/Docs" // GitHub names are case-insensitive
		}
		go func() {
			<-start
			_, err := svc.AddGitHubSource(ctx, "actor-1", business.AddGitHubSourceInput{
				OrgID: org, Repo: repo, BoundaryNodeID: seed.BoundaryNodeID,
			})
			errs <- err
		}()
	}
	close(start)

	var succeeded, refused int
	for range racers {
		err := <-errs
		switch status.Code(err) {
		case codes.OK:
			succeeded++
		case codes.AlreadyExists:
			refused++
		default:
			t.Fatalf("unexpected connect error: %v", err)
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, racers-1, refused)

	var sources []*business.DatasourceSource
	require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
		sources, err = testStore.ListDatasourceSources(ctx, org)
		return err
	}))
	require.Len(t, sources, 2, "the seed and exactly one acme/docs")
}
