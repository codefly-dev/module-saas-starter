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
//
// Auto-detection beats requiring callers to remember a `CompleteStep`
// call from every place the underlying action can happen: a user who
// creates their first org through the API never visits the onboarding
// page, but their progress should still reflect reality. Each
// detection is best-effort — failures don't abort the call, the worst
// case is the step keeps showing as pending until next refresh.
func (s *Service) GetProgress(ctx context.Context, userID string) (*OnboardingProgress, error) {
	w := wool.Get(ctx).In("GetProgress")

	steps, err := s.store.GetOnboardingProgress(ctx, userID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot get onboarding progress")
	}

	stepMap := make(map[string]*OnboardingStep, len(steps))
	for _, step := range steps {
		stepMap[step.StepName] = step
	}

	autoComplete := func(name string, detected bool) {
		if detected {
			now := time.Now()
			stepMap[name] = &OnboardingStep{
				StepName:    name,
				Status:      "completed",
				CompletedAt: &now,
			}
			_ = s.store.UpsertOnboardingStep(ctx, userID, name, "completed")
		}
	}

	pending := func(name string) bool {
		st, ok := stepMap[name]
		return !ok || st.Status == "pending"
	}

	// create_org — user belongs to at least one org. Set is the trigger;
	// our resolver auto-creates a personal org on first login so this
	// completes immediately for fixture users (which is fine — the wizard
	// step is "you have an org", not "you created one through the wizard").
	if pending("create_org") {
		orgs, oerr := s.store.ListOrganizationsForUser(ctx, userID)
		autoComplete("create_org", oerr == nil && len(orgs) > 0)
	}

	// invite_team — any invitation has been issued from any org the
	// user is a member of. Inviting a teammate is the user-visible
	// action; we detect by counting invitations across their orgs.
	if pending("invite_team") {
		orgs, _ := s.store.ListOrganizationsForUser(ctx, userID)
		invited := false
		for _, o := range orgs {
			n, _ := s.store.CountPendingInvitations(ctx, o.Id)
			if n > 0 {
				invited = true
				break
			}
			// Pending may have all been accepted — also consider any
			// status counts as "they've used invitations".
			invs, _ := s.store.ListInvitations(ctx, o.Id, "")
			if len(invs) > 0 {
				invited = true
				break
			}
		}
		autoComplete("invite_team", invited)
	}

	// choose_plan — user's primary org has any non-zero subscription.
	// We don't gate on plan tier; any active subscription means the
	// billing flow ran end-to-end at least once.
	if pending("choose_plan") {
		orgs, _ := s.store.ListOrganizationsForUser(ctx, userID)
		picked := false
		for _, o := range orgs {
			sub, serr := s.store.GetSubscription(ctx, o.Id)
			if serr == nil && sub != nil && sub.Status != "" {
				picked = true
				break
			}
		}
		autoComplete("choose_plan", picked)
	}

	// setup_api_key — at least one API key exists in any of the
	// user's orgs.
	if pending("setup_api_key") {
		orgs, _ := s.store.ListOrganizationsForUser(ctx, userID)
		hasKey := false
		for _, o := range orgs {
			_ = s.store.WithOrgTx(ctx, o.Id, func(ctx context.Context) error {
				keys, _, _ := s.store.ListAPIKeys(ctx, o.Id, 1, "")
				if len(keys) > 0 {
					hasKey = true
				}
				return nil
			})
			if hasKey {
				break
			}
		}
		autoComplete("setup_api_key", hasKey)
	}

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
