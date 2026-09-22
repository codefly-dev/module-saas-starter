package events

import (
	"context"

	eventsv1 "accounts/pkg/gen/saas/events/v1"
)

// Operations is the payload-free, cross-tenant administration boundary for the
// domain-event platform (#494 P3). It mirrors jobs.Operations: the
// implementation must run on the isolated app_job_worker database role so that
// request traffic never gains global access to event payloads or subscriptions.
// Only counts, timings, and control-plane subscription metadata cross it.
type Operations interface {
	GetEventOperations(context.Context, *eventsv1.GetEventOperationsRequest) (*eventsv1.GetEventOperationsResponse, error)
	ListEventSubscriptions(context.Context, *eventsv1.ListEventSubscriptionsRequest) (*eventsv1.ListEventSubscriptionsResponse, error)
}
