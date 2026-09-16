package infra

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

// RecordScopeNodeID resolves placement in the caller's existing tenant
// transaction. Authorization must precede disclosure of this metadata.
func (s *PostgresStore) RecordScopeNodeID(ctx context.Context, resourceType, resourceID string) (string, error) {
	var id string
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `SELECT id FROM scope_nodes WHERE resource_type=$1 AND resource_id=$2`, resourceType, resourceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}
