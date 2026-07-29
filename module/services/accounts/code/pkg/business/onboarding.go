package business

import (
	"context"
	"fmt"
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
	var completedTransitions []gen.OnboardingStepId
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
				current, err := s.store.EnsureOnboardingStep(
					ctx, userID, orgID, CurrentOnboardingFlowID, CurrentOnboardingFlowVersion, step,
				)
				if err != nil {
					return err
				}
				current.ID = definition.ID
				current.Prerequisites = append([]gen.OnboardingStepId(nil), definition.Prerequisites...)
				stepMap[definition.ID] = current
				step = current
			}
			step.Required = definition.Required
			step.Prerequisites = append([]gen.OnboardingStepId(nil), definition.Prerequisites...)
			step.LastSeenAt = now
			if detected[definition.ID] && step.Status == "pending" {
				completed := *step
				completed.Status = "completed"
				completed.CompletedAt = &now
				completed.CompletionMethod = "detected"
				current, transitioned, err := s.store.TransitionOnboardingStep(
					ctx,
					userID,
					orgID,
					CurrentOnboardingFlowID,
					CurrentOnboardingFlowVersion,
					"pending",
					&completed,
				)
				if err != nil {
					return err
				}
				current.ID = definition.ID
				current.Prerequisites = append([]gen.OnboardingStepId(nil), definition.Prerequisites...)
				stepMap[definition.ID] = current
				if transitioned {
					completedTransitions = append(completedTransitions, definition.ID)
					if err := s.captureProductEvent(
						ctx,
						"onboarding_step_completed",
						onboardingStepEventFactID(orgID, definition.ID, "completed"),
						userID,
						orgID,
						now,
						onboardingStepEventProperties(definition.ID),
					); err != nil {
						return err
					}
				}
			}
		}
		if len(completedTransitions) > 0 && onboardingChecklistComplete(stepMap) {
			return s.captureProductEvent(
				ctx,
				"onboarding_completed",
				onboardingFlowEventFactID(orgID),
				userID,
				orgID,
				now,
				map[string]any{"flow_version": fmt.Sprint(CurrentOnboardingFlowVersion)},
			)
		}
		return nil
	}); err != nil {
		return nil, w.Wrapf(err, "cannot reconcile onboarding progress")
	}
	for range completedTransitions {
		s.emit(ctx, userID, "user", "onboarding.step_completed", "organization", orgID, orgID)
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
	progress, err := s.GetProgress(ctx, userID, orgID)
	if err != nil {
		return err
	}
	var current *OnboardingStep
	for _, step := range progress.Steps {
		if step.ID == stepID {
			current = step
			break
		}
	}
	if current == nil {
		return wool.Get(ctx).NewError("invalid onboarding step")
	}
	if current.Status == "completed" {
		return wool.Get(ctx).NewError("completed onboarding steps cannot be skipped")
	}
	if current.Status == "skipped" {
		return nil
	}
	now := time.Now()
	step := &OnboardingStep{
		ID:               current.ID,
		StepName:         current.StepName,
		Status:           "skipped",
		Required:         current.Required,
		Prerequisites:    append([]gen.OnboardingStepId(nil), current.Prerequisites...),
		FirstSeenAt:      current.FirstSeenAt,
		SkippedAt:        &now,
		CompletionMethod: "user_skip",
		SkipReason:       strings.TrimSpace(reason),
		LastSeenAt:       now,
	}
	var transitioned bool
	if err := s.store.As(Identity{UserID: userID, OrgID: orgID}).Within(ctx, func(ctx context.Context) error {
		authoritative, changed, err := s.store.TransitionOnboardingStep(
			ctx,
			userID,
			orgID,
			CurrentOnboardingFlowID,
			CurrentOnboardingFlowVersion,
			"pending",
			step,
		)
		if err != nil {
			return err
		}
		transitioned = changed
		if !changed && authoritative.Status == "completed" {
			return wool.Get(ctx).NewError("completed onboarding steps cannot be skipped")
		}
		if !changed {
			return nil
		}
		if err := s.captureProductEvent(
			ctx,
			"onboarding_step_skipped",
			onboardingStepEventFactID(orgID, stepID, "skipped"),
			userID,
			orgID,
			now,
			onboardingStepEventProperties(stepID),
		); err != nil {
			return err
		}
		for _, progressStep := range progress.Steps {
			if progressStep.ID == stepID {
				progressStep = step
			}
			if progressStep.Status == "pending" && progressStep.ID != stepID {
				return nil
			}
		}
		return s.captureProductEvent(
			ctx,
			"onboarding_completed",
			onboardingFlowEventFactID(orgID),
			userID,
			orgID,
			now,
			map[string]any{"flow_version": fmt.Sprint(CurrentOnboardingFlowVersion)},
		)
	}); err != nil {
		return wool.Get(ctx).Wrapf(err, "cannot skip onboarding step")
	}
	if transitioned {
		s.emit(ctx, userID, "user", "onboarding.step_skipped", "organization", orgID, orgID)
	}
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
		if err := s.store.RecordOrganizationActivation(
			ctx, orgID, CurrentOnboardingFlowID, CurrentOnboardingFlowVersion, milestone, actorID,
		); err != nil {
			return err
		}
		return s.captureProductEvent(
			ctx,
			"activation_achieved",
			fmt.Sprintf(
				"%s:%s:%d:%s",
				orgID,
				CurrentOnboardingFlowID,
				CurrentOnboardingFlowVersion,
				milestone,
			),
			actorID,
			orgID,
			time.Now().UTC(),
			map[string]any{
				"definition_version": fmt.Sprint(CurrentOnboardingFlowVersion),
				"milestone":          milestone,
			},
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

func onboardingStepEventFactID(
	orgID string,
	stepID gen.OnboardingStepId,
	status string,
) string {
	return fmt.Sprintf(
		"%s:%s:%d:%s:%s",
		orgID,
		CurrentOnboardingFlowID,
		CurrentOnboardingFlowVersion,
		onboardingStepName(stepID),
		status,
	)
}

func onboardingFlowEventFactID(orgID string) string {
	return fmt.Sprintf(
		"%s:%s:%d",
		orgID,
		CurrentOnboardingFlowID,
		CurrentOnboardingFlowVersion,
	)
}

func onboardingStepEventProperties(stepID gen.OnboardingStepId) map[string]any {
	return map[string]any{
		"step_name":    onboardingStepName(stepID),
		"flow_version": fmt.Sprint(CurrentOnboardingFlowVersion),
	}
}

func onboardingChecklistComplete(
	steps map[gen.OnboardingStepId]*OnboardingStep,
) bool {
	for _, definition := range onboardingStepDefinitions {
		step := steps[definition.ID]
		if step == nil || step.Status == "pending" {
			return false
		}
	}
	return true
}
