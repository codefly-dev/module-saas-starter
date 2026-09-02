package business

import (
	"context"
	"errors"
	"time"
)

// ErrWorkContextAlreadyConsumed is returned when a SINGLE_USE Work Context is
// redeemed a second time. It is the fail-closed signal a consumer turns into a
// hard denial: the capability was already spent.
var ErrWorkContextAlreadyConsumed = errors.New("work context already consumed")

// WorkContextReplayStore is the durable replay store that makes the SINGLE_USE
// replay policy a real guarantee rather than a label. Enforcement sits on the
// consumer/verifier side: after a consumer cryptographically verifies a signed
// Work Context it claims the context id here, and the store admits exactly one
// claim per id. It stays a seam of its own — separate from issuance and from the
// consumer authority checks — so a deployment wires replay durability
// independently and no caller gains a broader mutation oracle.
type WorkContextReplayStore interface {
	// ConsumeSingleUseWorkContext records contextID as consumed and returns nil
	// on the first claim. Every later claim of the same id returns
	// ErrWorkContextAlreadyConsumed. expiresAt is the capability's sealed expiry:
	// the marker is retained until then so a replay is caught for the token's
	// whole live window, and reclaimed by GC once the token is expired anyway.
	ConsumeSingleUseWorkContext(ctx context.Context, orgID, contextID string, expiresAt time.Time) error
	// PurgeExpiredWorkContextReplays deletes consumed markers whose capability
	// has expired as of now, bounding the store to live tokens. It runs across
	// tenants under the control-plane role.
	PurgeExpiredWorkContextReplays(ctx context.Context, now time.Time) (int64, error)
}
