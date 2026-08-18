package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

type onboardingStoreFake struct {
	business.Store
	steps              map[string]*business.OnboardingStep
	pendingInvitations int32
}

func newOnboardingStoreFake() *onboardingStoreFake {
	return &onboardingStoreFake{steps: make(map[string]*business.OnboardingStep)}
}

func (f *onboardingStoreFake) As(identity business.Identity) business.Scoped {
	return waitlistScopedFake{identity: identity}
}

func (f *onboardingStoreFake) GetOnboardingProgress(
	context.Context,
	string,
	string,
	string,
	uint32,
) ([]*business.OnboardingStep, error) {
	steps := make([]*business.OnboardingStep, 0, len(f.steps))
	for _, step := range f.steps {
		copy := *step
		steps = append(steps, &copy)
	}
	return steps, nil
}

func (f *onboardingStoreFake) EnsureOnboardingStep(
	_ context.Context,
	_, _, _ string,
	_ uint32,
	step *business.OnboardingStep,
) (*business.OnboardingStep, error) {
	if current := f.steps[step.StepName]; current != nil {
		copy := *current
		return &copy, nil
	}
	copy := *step
	f.steps[step.StepName] = &copy
	return &copy, nil
}

func (f *onboardingStoreFake) TransitionOnboardingStep(
	_ context.Context,
	_, _, _ string,
	_ uint32,
	fromStatus string,
	step *business.OnboardingStep,
) (*business.OnboardingStep, bool, error) {
	current := f.steps[step.StepName]
	if current == nil {
		return nil, false, nil
	}
	if current.Status != fromStatus {
		copy := *current
		return &copy, false, nil
	}
	copy := *step
	f.steps[step.StepName] = &copy
	return &copy, true, nil
}

func (*onboardingStoreFake) GetOrganization(context.Context, string) (*gen.Organization, error) {
	return &gen.Organization{Name: "Acme", Slug: "acme"}, nil
}

func (f *onboardingStoreFake) CountPendingInvitations(context.Context, string) (int32, error) {
	return f.pendingInvitations, nil
}

func (*onboardingStoreFake) GetSubscription(context.Context, string) (*business.Subscription, error) {
	return nil, nil
}

func (*onboardingStoreFake) ListAPIKeys(context.Context, string, int32, string) ([]*gen.APIKey, string, error) {
	return nil, "", nil
}

func (*onboardingStoreFake) GetOrganizationActivation(context.Context, string, string, uint32, string) (*time.Time, error) {
	return nil, nil
}

func TestDetectedOnboardingCompletionEmitsExactlyOnce(t *testing.T) {
	store := newOnboardingStoreFake()
	service, err := business.NewService(store)
	require.NoError(t, err)
	audit := &recordingAuditEmitter{}
	service.SetAuditEmitter(audit)

	for range 2 {
		progress, err := service.GetProgress(context.Background(), "user-1", "org-1")
		require.NoError(t, err)
		require.True(t, progress.RequiredComplete)
	}
	for range 2 {
		require.NoError(t, service.CompleteStep(
			context.Background(),
			"user-1",
			"org-1",
			gen.OnboardingStepId_ONBOARDING_STEP_ID_CONFIGURE_ORGANIZATION,
		))
	}

	require.Len(t, audit.entries, 1)
	require.Equal(t, business.EventOnboardingStepDone, audit.entries[0].EventType)
}

func TestCompletedOptionalOnboardingStepCannotBeSkipped(t *testing.T) {
	store := newOnboardingStoreFake()
	store.pendingInvitations = 1
	service, err := business.NewService(store)
	require.NoError(t, err)
	audit := &recordingAuditEmitter{}
	service.SetAuditEmitter(audit)

	_, err = service.GetProgress(context.Background(), "user-1", "org-1")
	require.NoError(t, err)
	err = service.SkipStep(
		context.Background(),
		"user-1",
		"org-1",
		gen.OnboardingStepId_ONBOARDING_STEP_ID_INVITE_TEAM,
		"later",
	)

	require.ErrorContains(t, err, "completed onboarding steps cannot be skipped")
	require.Equal(t, "completed", store.steps["invite_team"].Status)
	for _, entry := range audit.entries {
		require.NotEqual(t, business.EventOnboardingStepSkip, entry.EventType)
	}
}
