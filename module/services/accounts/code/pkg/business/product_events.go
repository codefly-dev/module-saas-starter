package business

import (
	"context"

	"accounts/pkg/analytics"
	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"
)

func (s *Service) captureProductEvent(
	ctx context.Context,
	name string,
	factID string,
	actorUserID string,
	organizationID string,
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
		Properties:     properties,
	})
	if err != nil {
		return err
	}
	return s.productEvents.Capture(ctx, event)
}
