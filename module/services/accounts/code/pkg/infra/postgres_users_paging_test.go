//go:build !pure

package infra_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/infra/storetx"

	"github.com/stretchr/testify/require"
)

// The user listing resumes from the token it returns: the second page holds
// the user the first left out and returns no token. The three users are
// inserted in one transaction and share a creation time, so the walk also
// proves the uuid breaks the tie. Before this test's change the listing
// returned a token and never read one: every page was the first page, and a
// client following tokens looped.
func TestListUsersFollowsItsPageToken(t *testing.T) {
	ownerID := seedUser(t)
	orgID := seedOrg(t, ownerID)
	want := map[string]bool{}
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		for range 3 {
			id := business.NewIDString()
			want[id] = true
			if _, err := storetx.Tx(ctx).Exec(ctx,
				`INSERT INTO users (uuid, primary_email, status) VALUES ($1, $2, 'active')`, id, fmt.Sprintf("user-%s@test.local", id)); err != nil {
				return err
			}
		}
		return nil
	}))
	for id := range want {
		seedOrgMember(t, orgID, id)
	}
	page := func(org string, size int32, token string) (ids []string, next string) {
		require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
			users, nt, err := testStore.ListUsers(ctx, org, "", size, token)
			for _, user := range users {
				ids = append(ids, user.GetUuid())
			}
			next = nt
			return err
		}))
		return ids, next
	}

	first, token := page(orgID, 2, "")
	require.Len(t, first, 2)
	require.NotEmpty(t, token, "a third user remains, so the first page names where to resume")
	second, last := page(orgID, 2, token)
	require.Len(t, second, 1, "the second page holds the user the first left out, and nothing else")
	require.Empty(t, last, "nothing remains after the last user")
	seen := map[string]int{}
	for _, id := range append(first, second...) {
		seen[id]++
	}
	for id := range want {
		require.Equal(t, 1, seen[id], "user %s is listed exactly once", id)
	}

	// The platform-wide listing, which is what the method serves: following
	// tokens ends, and passes each of the three once.
	seen = map[string]int{}
	token = ""
	for pages := 0; ; pages++ {
		require.Less(t, pages, 10_000, "following page tokens must end")
		var ids []string
		ids, token = page("", 100, token)
		for _, id := range ids {
			seen[id]++
		}
		if token == "" {
			break
		}
	}
	for id := range want {
		require.Equal(t, 1, seen[id], "user %s is listed exactly once platform-wide", id)
	}

	err := testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		_, _, err := testStore.ListUsers(ctx, orgID, "", 2, "not a token this listing issued")
		return err
	})
	var storeErr *business.StoreError
	require.True(t, errors.As(err, &storeErr), "an unreadable token is refused, not read as the first page: %v", err)
	require.Equal(t, business.ErrTypeValidation, storeErr.StoreErrorType)
}
