package email

import "testing"

func TestResendDeliveryStatusMapping(t *testing.T) {
	cases := map[string]DeliveryStatus{
		"email.sent":             DeliveryStatusSent,
		"email.delivered":        DeliveryStatusDelivered,
		"email.failed":           DeliveryStatusBounced,
		"email.bounced":          DeliveryStatusBounced,
		"email.suppressed":       DeliveryStatusBounced,
		"email.complained":       DeliveryStatusComplained,
		"email.delivery_delayed": "",
		"email.opened":           "",
		"email.clicked":          "",
	}
	for eventType, want := range cases {
		if got := resendDeliveryStatus(eventType); got != want {
			t.Errorf("resendDeliveryStatus(%q) = %q, want %q", eventType, got, want)
		}
	}
}
