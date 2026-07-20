package auth

import (
	"context"

	"github.com/google/uuid"
)

type atomicSessionTransactionKey struct{}
type verifiedSessionIDKey struct{}

// WithAtomicSessionTransaction marks a context whose existing database
// transaction intentionally owns session issuance as part of a wider atomic
// state change. The MFA login consumer is currently the sole caller.
func WithAtomicSessionTransaction(ctx context.Context) context.Context {
	return context.WithValue(ctx, atomicSessionTransactionKey{}, true)
}

// HasAtomicSessionTransaction prevents SessionStore from reusing arbitrary
// transactions (especially privileged ones) merely because ctx contains a tx.
func HasAtomicSessionTransaction(ctx context.Context) bool {
	allowed, _ := ctx.Value(atomicSessionTransactionKey{}).(bool)
	return allowed
}

// WithVerifiedSessionID records a session id that was obtained from a locally
// verified access token or a trusted gateway assertion. Public handlers must
// never populate it directly from caller-controlled headers.
func WithVerifiedSessionID(ctx context.Context, sessionID uuid.UUID) context.Context {
	if sessionID == uuid.Nil {
		return ctx
	}
	return context.WithValue(ctx, verifiedSessionIDKey{}, sessionID)
}

func WithVerifiedSessionIDString(ctx context.Context, raw string) context.Context {
	sessionID, err := uuid.Parse(raw)
	if err != nil {
		return ctx
	}
	return WithVerifiedSessionID(ctx, sessionID)
}

func VerifiedSessionID(ctx context.Context) (uuid.UUID, bool) {
	sessionID, ok := ctx.Value(verifiedSessionIDKey{}).(uuid.UUID)
	return sessionID, ok && sessionID != uuid.Nil
}
