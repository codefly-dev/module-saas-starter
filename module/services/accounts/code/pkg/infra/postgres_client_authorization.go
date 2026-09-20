package infra

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
)

var _ business.ClientAuthorizationCodeStore = (*PostgresStore)(nil)

func (s *PostgresStore) CreateClientAuthorizationCode(ctx context.Context, code *business.ClientAuthorizationCode) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		INSERT INTO client_authorization_codes (
			id, code_hash, client_id, redirect_uri, code_challenge,
			user_id, session_id, expires_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		code.ID, code.CodeHash, code.ClientID, code.RedirectURI, code.CodeChallenge,
		code.UserID, code.SessionID, code.ExpiresAt, code.CreatedAt,
	)
	return err
}

// ConsumeClientAuthorizationCode uses a control-plane transaction because the
// caller holds only the random code, not a trusted user id. The exact hash
// predicate and the row lock are what constrain that elevation.
//
// The row is marked consumed before redeem is called, and it stays consumed
// even when redeem rejects the exchange: a code presented with the wrong
// verifier, redirect URI, or client is spent, not left for another attempt.
// redeem runs in the same transaction, so the client's new session and the
// code's consumption commit together or not at all.
func (s *PostgresStore) ConsumeClientAuthorizationCode(
	ctx context.Context,
	codeHash string,
	now time.Time,
	redeem func(context.Context, *business.ClientAuthorizationCode) error,
) error {
	rejected := false
	err := s.WithControlPlane(ctx, func(txCtx context.Context) error {
		q := s.getQueryExecutor(txCtx)
		var code business.ClientAuthorizationCode
		err := q.QueryRow(txCtx, `
			UPDATE client_authorization_codes
			   SET consumed_at = $2
			 WHERE code_hash = $1
			   AND consumed_at IS NULL
			   AND expires_at > $2
			RETURNING id, code_hash, client_id, redirect_uri, code_challenge,
			          user_id, session_id, expires_at, consumed_at, created_at`,
			codeHash, now).Scan(
			&code.ID, &code.CodeHash, &code.ClientID, &code.RedirectURI, &code.CodeChallenge,
			&code.UserID, &code.SessionID, &code.ExpiresAt, &code.ConsumedAt, &code.CreatedAt,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrClientAuthorizationRejected
			}
			return err
		}
		if err := redeem(txCtx, &code); err != nil {
			if errors.Is(err, auth.ErrClientAuthorizationRejected) {
				// Commit the consumption even though the public result is a
				// refusal. Returning the sentinel from this callback would roll
				// the UPDATE back and leave the code redeemable, so a caller
				// could keep guessing the verifier against a live code.
				rejected = true
				return nil
			}
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if rejected {
		return auth.ErrClientAuthorizationRejected
	}
	return nil
}
