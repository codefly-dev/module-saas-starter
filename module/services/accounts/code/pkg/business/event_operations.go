package business

import (
	"context"
	"errors"
	"fmt"

	eventsv1 "accounts/pkg/gen/saas/events/v1"
)

// ErrEventOperationsUnavailable is returned when the domain-event operations
// backend has not been wired (mirrors ErrJobOperationsUnavailable).
var ErrEventOperationsUnavailable = errors.New("event operations are not configured")

// GetEventOperations returns the payload-free administration snapshot of the
// domain-event platform: per-type counters, outbox relay health, and per-queue
// dead-letter counts. Read-only, super-admin only.
func (s *Service) GetEventOperations(
	ctx context.Context,
	actorID string,
	request *eventsv1.GetEventOperationsRequest,
) (*eventsv1.GetEventOperationsResponse, error) {
	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, fmt.Errorf("permission denied: %w", err)
	}
	if s.eventOperations == nil {
		return nil, ErrEventOperationsUnavailable
	}
	return s.eventOperations.GetEventOperations(ctx, request)
}

// ListEventSubscriptions returns every live (non-revoked) subscription across
// all principals with its per-queue dead-letter count. Read-only, super-admin
// only.
func (s *Service) ListEventSubscriptions(
	ctx context.Context,
	actorID string,
	request *eventsv1.ListEventSubscriptionsRequest,
) (*eventsv1.ListEventSubscriptionsResponse, error) {
	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, fmt.Errorf("permission denied: %w", err)
	}
	if s.eventOperations == nil {
		return nil, ErrEventOperationsUnavailable
	}
	return s.eventOperations.ListEventSubscriptions(ctx, request)
}
