package infra

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// The hop's admission key registrations.
//
// hop_admission_keys is a control-plane-owned platform relation (no tenant
// column, no RLS). Every lease is judged by the database's clock, never a
// replica's, so every replica publishes the same set. Every method assumes the
// caller opened a WithControlPlane transaction.

// RegisterAdmissionKey publishes key for lease from now, renewing it when it is
// already published, and answers whether this call began publishing it. Registrations are serialised by one transaction-scoped
// advisory lock, so two replicas registering different keys at the bound
// cannot both pass the count.
func (s *PostgresStore) RegisterAdmissionKey(ctx context.Context, key business.AdmissionKey, lease time.Duration, max int) (time.Time, bool, error) {
	q := s.getQueryExecutor(ctx)
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('hop-admission-keys',0))`); err != nil {
		return time.Time{}, false, err
	}
	if _, err := q.Exec(ctx, `DELETE FROM public.hop_admission_keys WHERE expires_at <= now()`); err != nil {
		return time.Time{}, false, err
	}
	var (
		others  int
		renewed bool
	)
	if err := q.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE key_id <> $1), COALESCE(bool_or(key_id = $1), false)
		  FROM public.hop_admission_keys`, key.ID).Scan(&others, &renewed); err != nil {
		return time.Time{}, false, err
	}
	if !renewed && others >= max {
		return time.Time{}, false, fmt.Errorf("%w: %d admission keys are already published; one must lapse before %s is registered",
			business.ErrAdmissionKeysFull, others, key.ID)
	}
	var (
		expires   time.Time
		published bool
	)
	// xmax is zero on a row this statement inserted and set on one it updated,
	// so it tells a first publication from a renewal.
	err := q.QueryRow(ctx, `
		INSERT INTO public.hop_admission_keys (key_id, public_key, expires_at)
		VALUES ($1, $2, now() + make_interval(secs => $3))
		ON CONFLICT (key_id) DO UPDATE SET expires_at = EXCLUDED.expires_at
		 WHERE public.hop_admission_keys.public_key = EXCLUDED.public_key
		RETURNING expires_at, xmax = 0`, key.ID, []byte(key.PublicKey), lease.Seconds()).Scan(&expires, &published)
	if errors.Is(err, pgx.ErrNoRows) {
		// No row means the id is published for another public key: the id is
		// derived from the key, so this is a collision, never a renewal.
		return time.Time{}, false, fmt.Errorf("%w: key id %s is published for another public key", business.ErrAdmissionKeyMalformed, key.ID)
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return expires, published, nil
}

// AdmissionKeys are the keys whose lease has not lapsed, by key id.
func (s *PostgresStore) AdmissionKeys(ctx context.Context) ([]business.AdmissionKey, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT key_id, public_key, expires_at FROM public.hop_admission_keys
		 WHERE expires_at > now() ORDER BY key_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []business.AdmissionKey
	for rows.Next() {
		var (
			key    business.AdmissionKey
			public []byte
		)
		if err := rows.Scan(&key.ID, &public, &key.ExpiresAt); err != nil {
			return nil, err
		}
		key.PublicKey = ed25519.PublicKey(public)
		out = append(out, key)
	}
	return out, rows.Err()
}

var _ business.AdmissionKeyStore = (*PostgresStore)(nil)
