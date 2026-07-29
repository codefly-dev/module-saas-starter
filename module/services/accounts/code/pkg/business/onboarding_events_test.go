package business_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/analytics"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"

	"github.com/stretchr/testify/require"
)

type onboardingEmitterFake struct {
	events []*analyticsv1.ProductEvent
}

const (
	onboardingEventUserID = "00000000-0000-4000-8000-000000000101"
	onboardingEventOrgID  = "00000000-0000-4000-8000-000000000102"
)

func (f *onboardingEmitterFake) Capture(
	_ context.Context,
	event *analyticsv1.ProductEvent,
) error {
	f.events = append(f.events, event)
	return nil
}

func (*onboardingEmitterFake) Suppress(
	context.Context,
	analytics.Suppression,
	analytics.CommandScope,
) error {
	return nil
}

func TestDetectedOnboardingEventsAreTransitionBasedAndIdempotent(t *testing.T) {
	now := time.Now()
	store := newOnboardingStoreFake()
	for _, stepName := range []string{"invite_team", "choose_plan", "setup_api_key"} {
		store.steps[stepName] = &business.OnboardingStep{
			StepName:    stepName,
			Status:      "skipped",
			FirstSeenAt: now,
			SkippedAt:   &now,
		}
	}
	service, err := business.NewService(store)
	require.NoError(t, err)
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	emitter := &onboardingEmitterFake{}
	service.SetProductAnalytics(registry, emitter)

	for range 2 {
		progress, err := service.GetProgress(
			t.Context(),
			onboardingEventUserID,
			onboardingEventOrgID,
		)
		require.NoError(t, err)
		require.True(t, progress.ChecklistComplete)
	}

	require.Len(t, emitter.events, 2)
	require.Equal(t, "onboarding_step_completed", emitter.events[0].GetEventName())
	require.Equal(
		t,
		analytics.DeterministicEventID(
			"onboarding_step_completed",
			onboardingEventOrgID+":starter_activation:1:configure_organization:completed",
		),
		emitter.events[0].GetEventId(),
	)
	require.Equal(t, "onboarding_completed", emitter.events[1].GetEventName())
	require.Equal(
		t,
		analytics.DeterministicEventID(
			"onboarding_completed",
			onboardingEventOrgID+":starter_activation:1",
		),
		emitter.events[1].GetEventId(),
	)
}

func TestSkippedOnboardingStepEmitsItsAuthoritativeTransition(t *testing.T) {
	store := newOnboardingStoreFake()
	service, err := business.NewService(store)
	require.NoError(t, err)
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	emitter := &onboardingEmitterFake{}
	service.SetProductAnalytics(registry, emitter)
	_, err = service.GetProgress(
		t.Context(),
		onboardingEventUserID,
		onboardingEventOrgID,
	)
	require.NoError(t, err)

	require.NoError(t, service.SkipStep(
		t.Context(),
		onboardingEventUserID,
		onboardingEventOrgID,
		gen.OnboardingStepId_ONBOARDING_STEP_ID_INVITE_TEAM,
		"later",
	))

	require.Len(t, emitter.events, 2)
	require.Equal(t, "onboarding_step_skipped", emitter.events[1].GetEventName())
	require.Equal(
		t,
		analytics.DeterministicEventID(
			"onboarding_step_skipped",
			onboardingEventOrgID+":starter_activation:1:invite_team:skipped",
		),
		emitter.events[1].GetEventId(),
	)
}
