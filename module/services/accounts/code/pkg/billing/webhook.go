package billing

// Stripe webhook handling.
//
// Security: Stripe signs every webhook with HMAC-SHA256 using the
// webhook secret registered in your dashboard. We verify the signature
// BEFORE parsing the body. Any unsigned or tampered payload is rejected.
//
// Idempotency: Stripe retries on any non-2xx response. Handlers MUST
// be idempotent. The db-backed event log (stripe_webhook_events) is
// inserted with a unique constraint on event id — duplicates short-
// circuit, returning 200 without re-processing.
//
// Events we care about for a subscription SaaS:
//
//   checkout.session.completed     → create local subscription row
//   customer.subscription.created  → same (either one is sufficient)
//   customer.subscription.updated  → plan change, status change, period
//   customer.subscription.deleted  → status=canceled
//   invoice.payment_failed         → status=past_due
//   invoice.payment_succeeded      → back to active if past_due
//
// Everything else is ignored (logged + ack'd 200).

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Event is the top-level envelope Stripe sends to the webhook endpoint.
// We only unmarshal the fields we route on; the full raw body stays in
// Data.Raw for downstream typed decoding.
type Event struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Created int64           `json:"created"`
	Data    json.RawMessage `json:"data"`
}

// ObjectFromData pulls the nested data.object out of an Event body.
// The caller unmarshals into the specific type.
func ObjectFromData(body []byte, into any) error {
	var envelope struct {
		Data struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	return json.Unmarshal(envelope.Data.Object, into)
}

// VerifySignature implements Stripe's v1 signing scheme:
// https://stripe.com/docs/webhooks#verify-manually.
//
// The Stripe-Signature header looks like:
//
//	t=1492774577,v1=5257a869e7ec...,v0=...
//
// We compute HMAC-SHA256 over `t + "." + body` with the webhook secret
// and constant-time compare against the v1 value. Both t drift and
// signature mismatch are treated as failures.
//
// toleranceSeconds=0 disables timestamp checking (tests only).
func VerifySignature(payload []byte, sigHeader, secret string, toleranceSeconds int64) error {
	if secret == "" {
		return errors.New("billing: webhook secret not configured")
	}
	ts, v1Sigs, err := parseStripeSignature(sigHeader)
	if err != nil {
		return err
	}
	if toleranceSeconds > 0 {
		now := time.Now().Unix()
		if now-ts > toleranceSeconds || ts-now > toleranceSeconds {
			return fmt.Errorf("billing: webhook timestamp outside tolerance (%d seconds)", toleranceSeconds)
		}
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))

	for _, sig := range v1Sigs {
		if hmac.Equal([]byte(expected), []byte(sig)) {
			return nil
		}
	}
	return errors.New("billing: webhook signature mismatch")
}

// parseStripeSignature teases apart the Stripe-Signature header value.
// Returns (timestamp, all v1 signatures, error).
func parseStripeSignature(header string) (int64, []string, error) {
	if header == "" {
		return 0, nil, errors.New("billing: missing Stripe-Signature header")
	}
	var (
		ts   int64
		v1s  []string
		seen bool
	)
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		switch key {
		case "t":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return 0, nil, fmt.Errorf("billing: invalid t in signature: %w", err)
			}
			ts = n
			seen = true
		case "v1":
			v1s = append(v1s, val)
		}
	}
	if !seen {
		return 0, nil, errors.New("billing: signature missing timestamp")
	}
	if len(v1s) == 0 {
		return 0, nil, errors.New("billing: signature missing v1 component")
	}
	return ts, v1s, nil
}

// ParseEvent verifies a signed payload and decodes the envelope.
// Tolerance default is 300 seconds (Stripe's recommended window).
func ParseEvent(payload []byte, sigHeader, secret string) (*Event, error) {
	if err := VerifySignature(payload, sigHeader, secret, 300); err != nil {
		return nil, err
	}
	var ev Event
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("billing: decode event: %w", err)
	}
	if ev.ID == "" || ev.Type == "" {
		return nil, errors.New("billing: event missing id or type")
	}
	return &ev, nil
}
