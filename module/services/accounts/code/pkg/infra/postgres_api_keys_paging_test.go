//go:build !pure

package infra_test

import (
	"context"
	"errors"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

// An organisation's keys past the first page are listed: the page token the
// listing returns is the position it resumes from, so every live key is seen
// exactly once and the last page returns no token. The three keys are made in
// one transaction and share a creation time, so the walk also proves the id
// breaks the tie. Before this test's change the token was ignored and never
// returned: only the newest page_size keys could be listed, or found to revoke.
func TestListAPIKeysFollowsItsPageToken(t *testing.T) {
	personID := seedUser(t)
	orgID := seedOrg(t, personID)
	want := map[string]bool{}
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		for range 3 {
			id := business.NewIDString()
			want[id] = true
			if err := testStore.CreateAPIKey(ctx, &gen.APIKey{Id: id, OrganizationId: orgID, UserId: personID, Name: "k", Prefix: "cfly_sk_live_"}, "paged-key-"+id); err != nil {
				return err
			}
		}
		return nil
	}))
	page := func(token string) (ids []string, next string) {
		require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			keys, nt, err := testStore.ListAPIKeys(ctx, orgID, 2, token)
			for _, key := range keys {
				ids = append(ids, key.GetId())
			}
			next = nt
			return err
		}))
		return ids, next
	}

	first, token := page("")
	require.Len(t, first, 2)
	require.NotEmpty(t, token, "a third key remains, so the first page names where to resume")

	second, last := page(token)
	require.Len(t, second, 1, "the second page holds the key the first left out, and nothing else")
	require.Empty(t, last, "nothing remains after the last key")
	for _, id := range append(first, second...) {
		require.True(t, want[id], "key %s is one of the three", id)
		delete(want, id)
	}
	require.Empty(t, want, "every key is listed exactly once")

	err := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, _, err := testStore.ListAPIKeys(ctx, orgID, 2, "not a token this listing issued")
		return err
	})
	var storeErr *business.StoreError
	require.True(t, errors.As(err, &storeErr), "an unreadable token is refused, not read as the first page: %v", err)
	require.Equal(t, business.ErrTypeValidation, storeErr.StoreErrorType)
}
