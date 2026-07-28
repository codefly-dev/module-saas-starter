package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

func TestConsentRejectsStalePolicyVersions(t *testing.T) {
	store := newWaitlistStoreFake()
	service, err := business.NewService(store)
	require.NoError(t, err)

	err = service.AcceptTerms(context.Background(), "user-1", "stale")
	require.Error(t, err)
	err = service.UpdateConsentPreferences(
		context.Background(), "user-1", "stale", true, true, "EU", "test",
	)
	require.Error(t, err)
}
