package business

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var ErrAuditIdempotencyConflict = errors.New("audit key already names a different intent")
var ErrAuditIdempotencyUnverifiable = errors.New("audit key predates verifiable effect receipts")

// AuditEffectFingerprint names the semantic effect, not the particular attempt.
// Generated event identity, delivery time, IP and catalog schema version are not
// caller intent. Attribution and the complete payload are. JSON map keys are
// sorted by encoding/json; unsupported payload values fail rather than hashing
// a lossy substitute. The version prefix freezes this interpretation for retries.
func AuditEffectFingerprint(entry AuditEntry) (string, error) {
	// The audit writer stores absent payloads as an empty JSON object.
	if entry.Payload == nil {
		entry.Payload = map[string]any{}
	}
	intent := struct {
		OrgID, EventType, ActorID, ActorType, Resource, ResourceID string
		ImpersonatedBy, ClientID                                   string
		IsImpersonated                                             bool
		Payload                                                    map[string]any
	}{entry.OrgID, string(entry.EventType), entry.ActorID, entry.ActorType,
		entry.Resource, entry.ResourceID, entry.ImpersonatedBy, entry.ClientID,
		entry.IsImpersonated, entry.Payload}
	raw, err := json.Marshal(intent)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "v1:" + hex.EncodeToString(sum[:]), nil
}

// A conflicting or historically unverifiable key cannot be retried as though
// the host merely lost a response. Preserve that distinction at the module API.
func moduleAuditEffectError(err error) error {
	if errors.Is(err, ErrAuditIdempotencyConflict) || errors.Is(err, ErrAuditIdempotencyUnverifiable) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}
