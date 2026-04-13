package billing_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"api/pkg/billing"
)

// signEvent produces a valid Stripe-Signature header for a payload.
// Mirrors the signing done on Stripe's side.
func signEvent(t *testing.T, payload []byte, secret string, ts int64) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

const testSecret = "whsec_test_abcdef123456"

// ============================================================================
// VerifySignature
// ============================================================================

func TestVerifySignature_Happy(t *testing.T) {
	payload := []byte(`{"id":"evt_01","type":"customer.subscription.created"}`)
	sig := signEvent(t, payload, testSecret, time.Now().Unix())
	err := billing.VerifySignature(payload, sig, testSecret, 300)
	require.NoError(t, err)
}

func TestVerifySignature_Tampered(t *testing.T) {
	payload := []byte(`{"id":"evt_01","type":"customer.subscription.created"}`)
	sig := signEvent(t, payload, testSecret, time.Now().Unix())

	tampered := []byte(`{"id":"evt_01","type":"invoice.payment_succeeded"}`)
	err := billing.VerifySignature(tampered, sig, testSecret, 300)
	require.Error(t, err)
	require.Contains(t, err.Error(), "signature mismatch")
}

func TestVerifySignature_WrongSecret(t *testing.T) {
	payload := []byte(`{}`)
	sig := signEvent(t, payload, testSecret, time.Now().Unix())

	err := billing.VerifySignature(payload, sig, "whsec_wrong", 300)
	require.Error(t, err)
	require.Contains(t, err.Error(), "signature mismatch")
}

func TestVerifySignature_MissingHeader(t *testing.T) {
	err := billing.VerifySignature([]byte(`{}`), "", testSecret, 300)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing Stripe-Signature header")
}

func TestVerifySignature_MalformedHeader(t *testing.T) {
	err := billing.VerifySignature([]byte(`{}`), "not-valid", testSecret, 300)
	require.Error(t, err)
}

func TestVerifySignature_MissingV1(t *testing.T) {
	err := billing.VerifySignature([]byte(`{}`),
		"t=123,v0=abc", testSecret, 300)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing v1")
}

func TestVerifySignature_StaleTimestamp(t *testing.T) {
	payload := []byte(`{}`)
	// Sign with a timestamp 10 minutes ago.
	sig := signEvent(t, payload, testSecret, time.Now().Add(-10*time.Minute).Unix())

	err := billing.VerifySignature(payload, sig, testSecret, 300) // 5 min tolerance
	require.Error(t, err)
	require.Contains(t, err.Error(), "timestamp outside tolerance")
}

func TestVerifySignature_FutureTimestamp(t *testing.T) {
	payload := []byte(`{}`)
	sig := signEvent(t, payload, testSecret, time.Now().Add(10*time.Minute).Unix())

	err := billing.VerifySignature(payload, sig, testSecret, 300)
	require.Error(t, err)
}

func TestVerifySignature_MultipleV1Accepts(t *testing.T) {
	// Stripe may send multiple v1 signatures during key rotation. Accept
	// the payload if ANY v1 matches.
	payload := []byte(`{"id":"evt"}`)
	ts := time.Now().Unix()

	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	valid := hex.EncodeToString(mac.Sum(nil))

	header := fmt.Sprintf("t=%d,v1=not_matching,v1=%s", ts, valid)
	err := billing.VerifySignature(payload, header, testSecret, 300)
	require.NoError(t, err)
}

// ============================================================================
// ParseEvent
// ============================================================================

func TestParseEvent_Happy(t *testing.T) {
	payload := []byte(`{
		"id": "evt_01ABC",
		"type": "customer.subscription.created",
		"created": 1700000000,
		"data": {"object": {"id": "sub_01", "status": "active"}}
	}`)
	sig := signEvent(t, payload, testSecret, time.Now().Unix())

	ev, err := billing.ParseEvent(payload, sig, testSecret)
	require.NoError(t, err)
	require.Equal(t, "evt_01ABC", ev.ID)
	require.Equal(t, "customer.subscription.created", ev.Type)
}

func TestParseEvent_RejectsUnsigned(t *testing.T) {
	_, err := billing.ParseEvent([]byte(`{"id":"evt","type":"x"}`), "", testSecret)
	require.Error(t, err)
}

func TestParseEvent_MissingID(t *testing.T) {
	payload := []byte(`{"type":"x"}`)
	sig := signEvent(t, payload, testSecret, time.Now().Unix())
	_, err := billing.ParseEvent(payload, sig, testSecret)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing id or type")
}

// ============================================================================
// ObjectFromData — typed decode of the nested data.object
// ============================================================================

func TestObjectFromData_Subscription(t *testing.T) {
	payload := []byte(`{
		"id":"evt","type":"customer.subscription.updated",
		"data":{"object":{"id":"sub_01","status":"past_due","customer":"cus_01","current_period_start":100,"current_period_end":200,"items":{"data":[{"price":{"id":"price_pro","product":"prod_pro"}}]}}}
	}`)
	var sub billing.Subscription
	require.NoError(t, billing.ObjectFromData(payload, &sub))
	require.Equal(t, "sub_01", sub.ID)
	require.Equal(t, "past_due", sub.Status)
	require.Equal(t, "price_pro", sub.PrimaryPriceID())
}

// ============================================================================
// Edge case: empty secret
// ============================================================================

func TestVerifySignature_EmptySecret(t *testing.T) {
	err := billing.VerifySignature([]byte(`{}`), "t=1,v1=x", "", 300)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "secret not configured"))
	require.False(t, errors.Is(err, nil))
}
