package business_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/analytics"
	"accounts/pkg/business"
	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type onboardingStoreFake struct {
	business.Store
	statuses map[string]string
}

func (f *onboardingStoreFake) As(identity business.Identity) business.Scoped {
	return &onboardingScopedFake{store: f, identity: identity}
}

func (f *onboardingStoreFake) GetOnboardingProgress(
	context.Context,
	string,
) ([]*business.OnboardingStep, error) {
	steps := make([]*business.OnboardingStep, 0, len(f.statuses))
	for name, status := range f.statuses {
		steps = append(steps, &business.OnboardingStep{StepName: name, Status: status})
	}
	return steps, nil
}

func (f *onboardingStoreFake) UpsertOnboardingStep(
	_ context.Context,
	_ string,
	stepName string,
	status string,
	_ time.Time,
) error {
	f.statuses[stepName] = status
	return nil
}

func (f *onboardingStoreFake) LockOnboardingProgress(context.Context, string) error {
	return nil
}

type onboardingScopedFake struct {
	business.Scoped
	store    *onboardingStoreFake
	identity business.Identity
}

func (f *onboardingScopedFake) Within(
	ctx context.Context,
	fn func(context.Context) error,
) error {
	return fn(ctx)
}

func (f *onboardingScopedFake) Identity() business.Identity {
	return f.identity
}

type onboardingEmitterFake struct {
	events []*analyticsv1.ProductEvent
}

func (f *onboardingEmitterFake) Capture(
	_ context.Context,
	event *analyticsv1.ProductEvent,
) error {
	f.events = append(f.events, event)
	return nil
}

func (f *onboardingEmitterFake) Suppress(
	context.Context,
	analytics.Suppression,
	analytics.CommandScope,
) error {
	return nil
}

func TestOnboardingEventsAreTransitionBasedAndIdempotent(t *testing.T) {
	store := &onboardingStoreFake{statuses: map[string]string{
		"create_org":  "completed",
		"invite_team": "completed",
		"choose_plan": "skipped",
	}}
	service, err := business.NewService(store)
	require.NoError(t, err)
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	emitter := &onboardingEmitterFake{}
	service.SetProductAnalytics(registry, emitter)
	userID := uuid.NewString()

	require.NoError(t, service.CompleteStep(t.Context(), userID, "setup_api_key"))
	require.NoError(t, service.CompleteStep(t.Context(), userID, "setup_api_key"))
	require.Len(t, emitter.events, 2)
	require.Equal(t, "onboarding_step_completed", emitter.events[0].GetEventName())
	require.Equal(t, "onboarding_completed", emitter.events[1].GetEventName())
	require.NotEqual(
		t,
		analytics.DeterministicEventID(
			"onboarding_step_completed",
			userID+":setup_api_key:completed",
		),
		emitter.events[0].GetEventId(),
	)

	firstCompletionID := emitter.events[0].GetEventId()
	require.NoError(t, service.SkipStep(t.Context(), userID, "setup_api_key"))
	require.NoError(t, service.CompleteStep(t.Context(), userID, "setup_api_key"))
	require.NotEqual(t, firstCompletionID, emitter.events[3].GetEventId())
}
