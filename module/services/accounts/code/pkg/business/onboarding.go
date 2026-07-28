package business

import (
	"context"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

const (
	CurrentOnboardingFlowID      = "starter_activation"
	CurrentOnboardingFlowVersion = 1
	CurrentOnboardingVariant     = "default"
)

type OnboardingStep struct {
	ID               gen.OnboardingStepId
	StepName         string
	Status           string
	Required         bool
	Prerequisites    []gen.OnboardingStepId
	FirstSeenAt      time.Time
	LastSeenAt       time.Time
	CompletedAt      *time.Time
	SkippedAt        *time.Time
	CompletionMethod string
	SkipReason       string
}

type OnboardingProgress struct {
	OrganizationID     string
	FlowID             string
	FlowVersion        uint32
	Variant            string
	Audience           string
	Persona            string
	Steps              []*OnboardingStep
	CurrentStep        gen.OnboardingStepId
	NextStep           gen.OnboardingStepId
	RequiredComplete   bool
	ChecklistComplete  bool
	ActivationAchieved bool
	StartedAt          *time.Time
	CompletedAt        *time.Time
	ActivatedAt        *time.Time
}

type onboardingStepDefinition struct {
	ID            gen.OnboardingStepId
	Required      bool
	Prerequisites []gen.OnboardingStepId
}

var onboardingStepDefinitions = []onboardingStepDefinition{
	{ID: gen.OnboardingStepId_ONBOARDING_STEP_ID_CONFIGURE_ORGANIZATION, Required: true},
	{ID: gen.OnboardingStepId_ONBOARDING_STEP_ID_INVITE_TEAM},
	{ID: gen.OnboardingStepId_ONBOARDING_STEP_ID_CHOOSE_PLAN},
	{ID: gen.OnboardingStepId_ONBOARDING_STEP_ID_SETUP_API_KEY},
}

func onboardingStepName(id gen.OnboardingStepId) string {
	return strings.ToLower(strings.TrimPrefix(id.String(), "ONBOARDING_STEP_ID_"))
}

func onboardingStepID(name string) gen.OnboardingStepId {
	value, ok := gen.OnboardingStepId_value["ONBOARDING_STEP_ID_"+strings.ToUpper(name)]
	if !ok {
		return gen.OnboardingStepId_ONBOARDING_STEP_ID_UNSPECIFIED
	}
	return gen.OnboardingStepId(value)
}

func (s *Service) GetProgress(ctx context.Context, userID, orgID string) (*OnboardingProgress, error) {
	w := wool.Get(ctx).In("GetProgress")
	identity := Identity{UserID: userID, OrgID: orgID}

	var stored []*OnboardingStep
	var organization *gen.Organization
	var pendingInvitations int32
	var subscription *Subscription
	var apiKeys []*gen.APIKey
	var activatedAt *time.Time

	err := s.store.As(identity).Within(ctx, func(ctx context.Context) error {
		var err error
		stored, err = s.store.GetOnboardingProgress(
			ctx, userID, orgID, CurrentOnboardingFlowID, CurrentOnboardingFlowVersion,
		)
		if err != nil {
			return err
		}
		organization, err = s.store.GetOrganization(ctx, orgID)
		if err != nil {
			return err
		}
		pendingInvitations, err = s.store.CountPendingInvitations(ctx, orgID)
		if err != nil {
			return err
		}
		subscription, err = s.store.GetSubscription(ctx, orgID)
		if err != nil {
			return err
		}
		apiKeys, _, err = s.store.ListAPIKeys(ctx, orgID, 1, "")
		if err != nil {
			return err
		}
		activatedAt, err = s.store.GetOrganizationActivation(
			ctx, orgID, CurrentOnboardingFlowID, CurrentOnboardingFlowVersion, "core_action",
		)
		return err
	})
	if err != nil {
		return nil, w.Wrapf(err, "cannot get onboarding progress")
	}

	stepMap := make(map[gen.OnboardingStepId]*OnboardingStep, len(stored))
	for _, step := range stored {
		step.ID = onboardingStepID(step.StepName)
		if step.ID != gen.OnboardingStepId_ONBOARDING_STEP_ID_UNSPECIFIED {
			stepMap[step.ID] = step
		}
	}

	detected := map[gen.OnboardingStepId]bool{
		gen.OnboardingStepId_ONBOARDING_STEP_ID_CONFIGURE_ORGANIZATION: organization != nil &&
			(organization.Name != "Personal" || !strings.HasPrefix(organization.Slug, "personal-")),
		gen.OnboardingStepId_ONBOARDING_STEP_ID_INVITE_TEAM:   pendingInvitations > 0,
		gen.OnboardingStepId_ONBOARDING_STEP_ID_CHOOSE_PLAN:   subscriptionCompletesOnboarding(subscription),
		gen.OnboardingStepId_ONBOARDING_STEP_ID_SETUP_API_KEY: len(apiKeys) > 0,
	}

	now := time.Now()
	if err := s.store.As(identity).Within(ctx, func(ctx context.Context) error {
		for _, definition := range onboardingStepDefinitions {
			step := stepMap[definition.ID]
			if step == nil {
				step = &OnboardingStep{
					ID:            definition.ID,
					StepName:      onboardingStepName(definition.ID),
					Status:        "pending",
					Required:      definition.Required,
					Prerequisites: append([]gen.OnboardingStepId(nil), definition.Prerequisites...),
					FirstSeenAt:   now,
					LastSeenAt:    now,
				}
				stepMap[definition.ID] = step
				if err := s.store.UpsertOnboardingStep(
					ctx, userID, orgID, CurrentOnboardingFlowID, CurrentOnboardingFlowVersion, step,
				); err != nil {
					return err
				}
			}
			step.Required = definition.Required
			step.Prerequisites = append([]gen.OnboardingStepId(nil), definition.Prerequisites...)
			step.LastSeenAt = now
			if detected[definition.ID] && step.Status == "pending" {
				step.Status = "completed"
				step.CompletedAt = &now
				step.CompletionMethod = "detected"
				if err := s.store.UpsertOnboardingStep(
					ctx, userID, orgID, CurrentOnboardingFlowID, CurrentOnboardingFlowVersion, step,
				); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return nil, w.Wrapf(err, "cannot reconcile onboarding progress")
	}

	progress := &OnboardingProgress{
		OrganizationID:     orgID,
		FlowID:             CurrentOnboardingFlowID,
		FlowVersion:        CurrentOnboardingFlowVersion,
		Variant:            CurrentOnboardingVariant,
		RequiredComplete:   true,
		ChecklistComplete:  true,
		ActivationAchieved: activatedAt != nil,
		ActivatedAt:        activatedAt,
	}
	for _, definition := range onboardingStepDefinitions {
		step := stepMap[definition.ID]
		progress.Steps = append(progress.Steps, step)
		if progress.StartedAt == nil || step.FirstSeenAt.Before(*progress.StartedAt) {
			started := step.FirstSeenAt
			progress.StartedAt = &started
		}
		if definition.Required && step.Status != "completed" {
			progress.RequiredComplete = false
		}
		if step.Status == "pending" {
			progress.ChecklistComplete = false
			if progress.CurrentStep == gen.OnboardingStepId_ONBOARDING_STEP_ID_UNSPECIFIED {
				progress.CurrentStep = definition.ID
			} else if progress.NextStep == gen.OnboardingStepId_ONBOARDING_STEP_ID_UNSPECIFIED {
				progress.NextStep = definition.ID
			}
		}
	}
	if progress.ChecklistComplete {
		var completed time.Time
		for _, step := range progress.Steps {
			at := step.CompletedAt
			if at == nil {
				at = step.SkippedAt
			}
			if at != nil && at.After(completed) {
				completed = *at
			}
		}
		progress.CompletedAt = &completed
	}
	return progress, nil
}

func (s *Service) CompleteStep(
	ctx context.Context,
	userID, orgID string,
	stepID gen.OnboardingStepId,
) error {
	if !validOnboardingStep(stepID) {
		return wool.Get(ctx).NewError("invalid onboarding step")
	}
	progress, err := s.GetProgress(ctx, userID, orgID)
	if err != nil {
		return err
	}
	for _, step := range progress.Steps {
		if step.ID == stepID {
			if step.Status != "completed" {
				return wool.Get(ctx).NewError("complete the represented product action first")
			}
			s.emit(ctx, userID, "user", "onboarding.step_completed", "organization", orgID, orgID)
			return nil
		}
	}
	return wool.Get(ctx).NewError("invalid onboarding step")
}

func (s *Service) SkipStep(
	ctx context.Context,
	userID, orgID string,
	stepID gen.OnboardingStepId,
	reason string,
) error {
	if !validOnboardingStep(stepID) {
		return wool.Get(ctx).NewError("invalid onboarding step")
	}
	for _, definition := range onboardingStepDefinitions {
		if definition.ID == stepID && definition.Required {
			return wool.Get(ctx).NewError("required onboarding steps cannot be skipped")
		}
	}
	now := time.Now()
	step := &OnboardingStep{
		ID:               stepID,
		StepName:         onboardingStepName(stepID),
		Status:           "skipped",
		FirstSeenAt:      now,
		SkippedAt:        &now,
		CompletionMethod: "user_skip",
		SkipReason:       strings.TrimSpace(reason),
		LastSeenAt:       now,
	}
	if err := s.store.As(Identity{UserID: userID, OrgID: orgID}).Within(ctx, func(ctx context.Context) error {
		return s.store.UpsertOnboardingStep(
			ctx, userID, orgID, CurrentOnboardingFlowID, CurrentOnboardingFlowVersion, step,
		)
	}); err != nil {
		return wool.Get(ctx).Wrapf(err, "cannot skip onboarding step")
	}
	s.emit(ctx, userID, "user", "onboarding.step_skipped", "organization", orgID, orgID)
	return nil
}

func (s *Service) RecordProductActivation(
	ctx context.Context,
	actorID, orgID, milestone string,
) error {
	if milestone == "" {
		milestone = "core_action"
	}
	err := s.store.As(Identity{UserID: actorID, OrgID: orgID}).Within(ctx, func(ctx context.Context) error {
		return s.store.RecordOrganizationActivation(
			ctx, orgID, CurrentOnboardingFlowID, CurrentOnboardingFlowVersion, milestone, actorID,
		)
	})
	if err == nil {
		s.emit(ctx, actorID, "user", "activation.achieved", "organization", orgID, orgID)
	}
	return err
}

func validOnboardingStep(id gen.OnboardingStepId) bool {
	for _, definition := range onboardingStepDefinitions {
		if definition.ID == id {
			return true
		}
	}
	return false
}

func subscriptionCompletesOnboarding(subscription *Subscription) bool {
	return subscription != nil &&
		(subscription.Status == "active" || subscription.Status == "trialing")
}
