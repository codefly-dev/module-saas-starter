package events_test

import (
	"testing"
	"time"

	"accounts/pkg/events"
	"accounts/pkg/events/eventstest"
)

func TestFakeTransportConformance(t *testing.T) {
	const lease = 300 * time.Millisecond
	eventstest.RunConformance(t, eventstest.Harness{
		New: func(_ *testing.T, subscriptions []events.Subscription) events.Transport {
			return events.NewFakeTransport(subscriptions, lease)
		},
		LeaseDuration: lease,
	})
}
