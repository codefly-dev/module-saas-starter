//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// The catalogue says whether a target's applied generation declares pages, read
// from the registration's frontend half — the record the navigation is built
// from — so an administrator is offered access to an installation for its pages
// without the page guessing from a name or a kind.
func TestAvailableSolutionTargetReportsDeclaredPages(t *testing.T) {
	alias := uniqueAlias("example-pages")
	target := declarePresence(t, "acme.test."+alias, alias)

	pages := func() bool {
		t.Helper()
		var hasPages bool
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			targets, err := testStore.ListAvailableSolutionTargets(ctx, business.AvailableSolutionQuery{TargetID: target, Limit: 1})
			if err != nil {
				return err
			}
			require.Len(t, targets, 1)
			hasPages = targets[0].HasPages
			return nil
		}))
		return hasPages
	}

	require.False(t, pages(), "a headless generation declares no pages")

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		record, err := testStore.GetSolutionRegistrationForUpdate(ctx, alias)
		if err != nil {
			return err
		}
		revision, err := testStore.NextSolutionRegistryRevision(ctx)
		if err != nil {
			return err
		}
		record.Revision = revision
		record.Frontend = &business.SolutionFrontendHalf{Revision: revision, Manifest: `{"title":"Example"}`}
		return testStore.SaveSolutionRegistration(ctx, record)
	}))
	require.True(t, pages(), "a generation with a frontend block declares pages")
}
