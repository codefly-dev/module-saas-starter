package redisstate

import (
	"context"
	"time"
)

const oauthNoncePrefix = "oauth-nonce:"

// OAuthNonceConsumer records single-use OAuth-state nonces in shared Redis state
// so a captured state cannot be replayed across replicas within its TTL. It
// satisfies auth.NonceConsumer.
type OAuthNonceConsumer struct {
	state State
}

// NewOAuthNonceConsumer wraps State as the OAuth-state one-shot list.
func NewOAuthNonceConsumer(s State) *OAuthNonceConsumer {
	return &OAuthNonceConsumer{state: s}
}

// Consume atomically increments the nonce's counter and arms its TTL on the
// first increment. A result of 1 is the fresh use; any higher value is a
// replay. The atomic Incr means two concurrent callback attempts for the same
// state can't both see a fresh use.
func (c *OAuthNonceConsumer) Consume(ctx context.Context, nonce string, ttl time.Duration) (bool, error) {
	if nonce == "" {
		return false, nil
	}
	count, err := c.state.Incr(ctx, oauthNoncePrefix+nonce, ttl)
	if err != nil {
		return false, err
	}
	return count == 1, nil
}
