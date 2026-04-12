package business

import (
	"context"
	"time"

	"github.com/codefly-dev/core/wool"
)

// OnboardingStep represents a single step in the onboarding flow.
type OnboardingStep struct {
	StepName    string
	Status      string // "pending", "completed", "skipped"
	CompletedAt *time.Time
}

// OnboardingProgress aggregates all onboarding steps for a user.
type OnboardingProgress struct {
	Steps     []*OnboardingStep
	Completed bool
}

// OnboardingStepNames defines the ordered set of onboarding steps.
var OnboardingStepNames = []string{
	"create_org",
	"invite_team",
	"choose_plan",
	"setup_api_key",
}

// GetProgress returns the onboarding progress for a user, auto-detecting
// steps that have already been completed by normal product usage.
func (s *Service) GetProgress(ctx context.Context, userID string) (*OnboardingProgress, error) {
	w := wool.Get(ctx).In("GetProgress")

	steps, err := s.store.GetOnboardingProgress(ctx, userID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot get onboarding progress")
	}

	// Build a lookup map of existing steps
	stepMap := make(map[string]*OnboardingStep, len(steps))
	for _, step := range steps {
		stepMap[step.StepName] = step
	}

	// Auto-detect: if user has an org (beyond the default personal one), mark create_org completed
	if _, ok := stepMap["create_org"]; !ok || stepMap["create_org"].Status == "pending" {
		orgs, err := s.store.ListOrganizationsForUser(ctx, userID)
		if err == nil && len(orgs) > 0 {
			// User has at least one org — auto-complete
			now := time.Now()
			step := &OnboardingStep{
				StepName:    "create_org",
				Status:      "completed",
				CompletedAt: &now,
			}
			stepMap["create_org"] = step
			// Best-effort persist
			_ = s.store.UpsertOnboardingStep(ctx, userID, "create_org", "completed")
		}
	}

	// Build the complete ordered list, filling in missing steps as pending
	var result []*OnboardingStep
	allDone := true
	for _, name := range OnboardingStepNames {
		if step, ok := stepMap[name]; ok {
			result = append(result, step)
			if step.Status == "pending" {
				allDone = false
			}
		} else {
			result = append(result, &OnboardingStep{
				StepName: name,
				Status:   "pending",
			})
			allDone = false
		}
	}

	return &OnboardingProgress{
		Steps:     result,
		Completed: allDone,
	}, nil
}

// CompleteStep marks an onboarding step as completed.
func (s *Service) CompleteStep(ctx context.Context, userID, stepName string) error {
	w := wool.Get(ctx).In("CompleteStep")

	if !isValidStepName(stepName) {
		return w.NewError("invalid onboarding step: %s", stepName)
	}

	if err := s.store.UpsertOnboardingStep(ctx, userID, stepName, "completed"); err != nil {
		return w.Wrapf(err, "cannot complete onboarding step")
	}

	return nil
}

// SkipStep marks an onboarding step as skipped.
func (s *Service) SkipStep(ctx context.Context, userID, stepName string) error {
	w := wool.Get(ctx).In("SkipStep")

	if !isValidStepName(stepName) {
		return w.NewError("invalid onboarding step: %s", stepName)
	}

	if err := s.store.UpsertOnboardingStep(ctx, userID, stepName, "skipped"); err != nil {
		return w.Wrapf(err, "cannot skip onboarding step")
	}

	return nil
}

// IsOnboardingComplete checks if all onboarding steps are either completed or skipped.
func (s *Service) IsOnboardingComplete(ctx context.Context, userID string) (bool, error) {
	progress, err := s.GetProgress(ctx, userID)
	if err != nil {
		return false, err
	}
	return progress.Completed, nil
}

func isValidStepName(name string) bool {
	for _, s := range OnboardingStepNames {
		if s == name {
			return true
		}
	}
	return false
}
