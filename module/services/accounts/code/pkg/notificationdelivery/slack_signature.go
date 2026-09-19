package notificationdelivery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// VerifySlackSignature authenticates the exact bytes before JSON decoding.
// A receiver must still bind the installation to its tenant and durably accept
// event_id before acknowledging. Signature verification alone is not delivery
// deduplication or tenant authorization.
func VerifySlackSignature(secret, timestamp, signature string, body []byte, now time.Time) bool {
	if secret == "" || len(signature) != 67 || signature[:3] != "v0=" {
		return false
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	current := now.Unix()
	if seconds < current-300 || seconds > current+300 {
		return false
	}
	claimed, err := hex.DecodeString(signature[3:])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":"))
	_, _ = mac.Write(body)
	return hmac.Equal(claimed, mac.Sum(nil))
}
