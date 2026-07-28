package business

import (
	"context"
	"time"

	"github.com/codefly-dev/core/wool"

	gen "accounts/pkg/gen/saas/accounts/v1"
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
// Detection reads are best-effort. Once a completed fact is detected, its
// progress transition and analytics command commit atomically.
func (s *Service) GetProgress(ctx context.Context, userID string) (*OnboardingProgress, error) {
	w := wool.Get(ctx).In("GetProgress")

	var steps []*OnboardingStep
	err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		var e error
		steps, e = s.store.GetOnboardingProgress(ctx, userID)
		return e
	})
	if err != nil {
		return nil, w.Wrapf(err, "cannot get onboarding progress")
	}

	stepMap := make(map[string]*OnboardingStep, len(steps))
	for _, step := range steps {
		stepMap[step.StepName] = step
	}

	autoComplete := func(name string, detected bool) error {
		if !detected {
			return nil
		}
		occurredAt := time.Now().UTC()
		if err := s.transitionOnboardingStep(
			ctx,
			userID,
			name,
			"completed",
			occurredAt,
		); err != nil {
			return err
		}
		stepMap[name] = &OnboardingStep{
			StepName:    name,
			Status:      "completed",
			CompletedAt: &occurredAt,
		}
		return nil
	}

	pending := func(name string) bool {
		st, ok := stepMap[name]
		return !ok || st.Status == "pending"
	}

	// listOrgs is shared across the steps — ListOrganizationsForUser
	// is cross-tenant by definition (the user can be in many orgs)
	// so it runs under WithControlPlane. Org-scoped reads below re-enter
	// WithOrgTx for each org.
	listOrgs := func() []*gen.Organization {
		var orgs []*gen.Organization
		_ = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			os, err := s.store.ListOrganizationsForUser(ctx, userID)
			orgs = os
			return err
		})
		return orgs
	}

	// create_org — user belongs to at least one org. Set is the trigger;
	// our resolver auto-creates a personal org on first login so this
	// completes immediately for fixture users (which is fine — the wizard
	// step is "you have an org", not "you created one through the wizard").
	if pending("create_org") {
		orgs := listOrgs()
		if err := autoComplete("create_org", len(orgs) > 0); err != nil {
			return nil, w.Wrapf(err, "cannot auto-complete create_org")
		}
	}

	// invite_team — any invitation has been issued from any org the
	// user is a member of. Inviting a teammate is the user-visible
	// action; we detect by counting invitations across their orgs.
	if pending("invite_team") {
		orgs := listOrgs()
		invited := false
		for _, o := range orgs {
			_ = s.store.WithOrgTx(ctx, o.Id, func(ctx context.Context) error {
				if n, _ := s.store.CountPendingInvitations(ctx, o.Id); n > 0 {
					invited = true
					return nil
				}
				if invs, _ := s.store.ListInvitations(ctx, o.Id, ""); len(invs) > 0 {
					invited = true
				}
				return nil
			})
			if invited {
				break
			}
		}
		if err := autoComplete("invite_team", invited); err != nil {
			return nil, w.Wrapf(err, "cannot auto-complete invite_team")
		}
	}

	// choose_plan — user's primary org has any non-zero subscription.
	// We don't gate on plan tier; any active subscription means the
	// billing flow ran end-to-end at least once.
	if pending("choose_plan") {
		orgs := listOrgs()
		picked := false
		for _, o := range orgs {
			_ = s.store.WithOrgTx(ctx, o.Id, func(ctx context.Context) error {
				sub, serr := s.store.GetSubscription(ctx, o.Id)
				if serr == nil && sub != nil && sub.Status != "" {
					picked = true
				}
				return nil
			})
			if picked {
				break
			}
		}
		if err := autoComplete("choose_plan", picked); err != nil {
			return nil, w.Wrapf(err, "cannot auto-complete choose_plan")
		}
	}

	// setup_api_key — at least one API key exists in any of the
	// user's orgs.
	if pending("setup_api_key") {
		orgs := listOrgs()
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
		if err := autoComplete("setup_api_key", hasKey); err != nil {
			return nil, w.Wrapf(err, "cannot auto-complete setup_api_key")
		}
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
	return s.transitionOnboardingStep(ctx, userID, stepName, "completed", time.Now().UTC())
}

// SkipStep marks an onboarding step as skipped.
func (s *Service) SkipStep(ctx context.Context, userID, stepName string) error {
	return s.transitionOnboardingStep(ctx, userID, stepName, "skipped", time.Now().UTC())
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

func (s *Service) transitionOnboardingStep(
	ctx context.Context,
	userID string,
	stepName string,
	status string,
	occurredAt time.Time,
) error {
	w := wool.Get(ctx).In("TransitionOnboardingStep")
	if !isValidStepName(stepName) {
		return w.NewError("invalid onboarding step: %s", stepName)
	}
	if err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		if err := s.store.LockOnboardingProgress(ctx, userID); err != nil {
			return err
		}
		steps, err := s.store.GetOnboardingProgress(ctx, userID)
		if err != nil {
			return err
		}
		current := make(map[string]string, len(steps))
		for _, step := range steps {
			current[step.StepName] = step.Status
		}
		if current[stepName] == status {
			return nil
		}
		wasComplete := onboardingStepsComplete(current)
		if err := s.store.UpsertOnboardingStep(
			ctx,
			userID,
			stepName,
			status,
			occurredAt,
		); err != nil {
			return err
		}
		current[stepName] = status
		if err := s.captureProductEvent(
			ctx,
			"onboarding_step_"+status,
			userID+":"+stepName+":"+status+":"+occurredAt.Format(time.RFC3339Nano),
			userID,
			"",
			occurredAt,
			map[string]any{"step_name": stepName, "flow_version": "v1"},
		); err != nil {
			return err
		}
		if wasComplete || !onboardingStepsComplete(current) {
			return nil
		}
		return s.captureProductEvent(
			ctx,
			"onboarding_completed",
			userID+":v1:"+occurredAt.Format(time.RFC3339Nano),
			userID,
			"",
			occurredAt,
			map[string]any{
				"flow_version": "v1",
			},
		)
	}); err != nil {
		return w.Wrapf(err, "cannot transition onboarding step")
	}
	return nil
}

func onboardingStepsComplete(statuses map[string]string) bool {
	for _, name := range OnboardingStepNames {
		if statuses[name] != "completed" && statuses[name] != "skipped" {
			return false
		}
	}
	return true
}
