package business

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubscriptionCompletesOnboardingOnlyForCurrentUsablePlans(t *testing.T) {
	for _, status := range []string{"active", "trialing"} {
		require.True(t, subscriptionCompletesOnboarding(&Subscription{Status: status}))
	}
	for _, status := range []string{"", "incomplete", "past_due", "canceled"} {
		require.False(t, subscriptionCompletesOnboarding(&Subscription{Status: status}))
	}
	require.False(t, subscriptionCompletesOnboarding(nil))

	// The free plan attached at organization creation is an active
	// subscription like any other, so it satisfies CHOOSE_PLAN immediately —
	// no paid tier or Stripe subscription id is required.
	require.True(t, subscriptionCompletesOnboarding(&Subscription{
		PlanID: "free-plan-id",
		Status: "active",
	}))
}
