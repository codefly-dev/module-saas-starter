package business

import (
	"context"
	"time"

	"accounts/pkg/analytics"
	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"
)

func (s *Service) captureProductEvent(
	ctx context.Context,
	name string,
	factID string,
	actorUserID string,
	organizationID string,
	occurredAt time.Time,
	properties map[string]any,
) error {
	if s.productEvents == nil || s.eventRegistry == nil {
		return nil
	}
	event, err := s.eventRegistry.NewEvent(analytics.NewEventInput{
		EventID:        analytics.DeterministicEventID(name, factID),
		Name:           name,
		ActorUserID:    actorUserID,
		OrganizationID: organizationID,
		Source:         analyticsv1.EventSource_EVENT_SOURCE_API,
		OccurredAt:     occurredAt,
		Properties:     properties,
	})
	if err != nil {
		return err
	}
	return s.productEvents.Capture(ctx, event)
}

func (s *Service) suppressProductIdentity(
	ctx context.Context,
	suppression analytics.Suppression,
	access Identity,
) error {
	if s.productEvents == nil {
		return nil
	}
	var scope analytics.CommandScope
	switch {
	case access.OrgID != "":
		scope = analytics.TenantScope(access.OrgID)
	case access.UserID != "":
		scope = analytics.SubjectScope(access.UserID)
	case access.IsSystem():
		scope = analytics.GlobalScope()
	default:
		return analytics.ErrCommandScopeRequired
	}
	return s.productEvents.Suppress(ctx, suppression, scope)
}

func userAnalyticsSuppression(userID string) analytics.Suppression {
	return analytics.Suppression{
		CommandID: analytics.DeterministicEventID("identity_suppressed", "user", userID),
		UserID:    userID,
	}
}
