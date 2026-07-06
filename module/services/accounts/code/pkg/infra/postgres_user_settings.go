package infra

import (
	"context"
)

// GetUserSettings returns the user's settings JSONB as raw bytes
// for the business layer to unmarshal. Empty rows ('{}') and
// missing-user errors are surfaced as-is — the business layer
// converts {}-bytes to an empty struct.
func (s *PostgresStore) GetUserSettings(ctx context.Context, userID string) ([]byte, error) {
	q := s.getQueryExecutor(ctx)
	var raw []byte
	err := q.QueryRow(ctx, `
		SELECT settings FROM users WHERE uuid = $1`, userID,
	).Scan(&raw)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// UpdateUserSettings merges a partial JSONB patch onto the stored
// settings via the `||` (concatenation) operator. Last-write-wins
// per top-level key: incoming { theme: "dark", email: {...} } merges
// onto stored { locale: "en", theme: "system" } as { locale: "en",
// theme: "dark", email: {...} } — nested objects are replaced, not
// merged. The FE accommodates by sending the full nested object on
// any nested-key change.
//
// Why not jsonb_set with a recursive merge: postgres has no
// idiomatic deep-merge operator. The shallow `||` keeps the
// semantic explicit and the FE simple.
func (s *PostgresStore) UpdateUserSettings(ctx context.Context, userID string, patch []byte) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		UPDATE users
		   SET settings = settings || $2::jsonb,
		       updated_at = NOW()
		 WHERE uuid = $1`, userID, patch)
	return err
}
