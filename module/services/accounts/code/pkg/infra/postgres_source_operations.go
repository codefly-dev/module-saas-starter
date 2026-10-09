package infra

import (
	"accounts/pkg/business"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/datasource/operations"
)

// ReplaceSourceOperations is called inside one org transaction. Locking the
// source serialises concurrent whole-set replacements and token rotation.
func (s *PostgresStore) ReplaceSourceOperations(ctx context.Context, org, source string, declarations []operations.Declaration) error {
	if _, err := s.LockDatasourceSourceCredentialRef(ctx, org, source); err != nil {
		return err
	}
	q := s.getQueryExecutor(ctx)
	if _, err := q.Exec(ctx, `DELETE FROM datasource_source_operations WHERE org_id=$1 AND source_id=$2`, org, source); err != nil {
		return err
	}
	for _, d := range declarations {
		query := d.Query
		if query == nil {
			query = []string{}
		}
		_, err := q.Exec(ctx, `INSERT INTO datasource_source_operations
  (org_id,source_id,name,description,method,path,query,input_schema,output_schema,effect,max_output_bytes,digest)
  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, org, source, d.Name, d.Description, d.Method, d.Path, query, d.InputSchema, d.OutputSchema, d.Effect, d.MaxOutputBytes, d.Digest)
		if err != nil {
			return err
		}
	}
	return nil
}
func (s *PostgresStore) ListSourceOperations(ctx context.Context, org, source string) ([]operations.Declaration, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `SELECT name,description,method,path,query,input_schema,output_schema,effect,max_output_bytes,digest FROM datasource_source_operations WHERE org_id=$1 AND source_id=$2 ORDER BY name`, org, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []operations.Declaration{}
	for rows.Next() {
		var d operations.Declaration
		if err := rows.Scan(&d.Name, &d.Description, &d.Method, &d.Path, &d.Query, &d.InputSchema, &d.OutputSchema, &d.Effect, &d.MaxOutputBytes, &d.Digest); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *PostgresStore) ConsumeDatasourceOAuthState(ctx context.Context, org, hash string, expiry time.Time) (bool, error) {
	q := s.getQueryExecutor(ctx)
	if _, err := q.Exec(ctx, `DELETE FROM datasource_oauth_consumed_states WHERE org_id=$1 AND expires_at < now()`, org); err != nil {
		return false, err
	}
	out, err := q.Exec(ctx, `INSERT INTO datasource_oauth_consumed_states (org_id,state_hash,expires_at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, org, hash, expiry)
	return out.RowsAffected() == 1, err
}

func (s *PostgresStore) GetSourceOperationAttempt(ctx context.Context, org, effect string) (*business.SourceOperationAttempt, error) {
	var a business.SourceOperationAttempt
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `SELECT org_id,effect_id,actor_id,source_id,operation,declaration_digest,request_digest FROM datasource_operation_attempts WHERE org_id=$1 AND effect_id=$2`, org, effect).Scan(&a.OrgID, &a.EffectID, &a.ActorID, &a.SourceID, &a.Operation, &a.DeclarationDigest, &a.RequestDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}
func (s *PostgresStore) CreateSourceOperationAttempt(ctx context.Context, a business.SourceOperationAttempt) (bool, error) {
	tag, err := s.getQueryExecutor(ctx).Exec(ctx, `INSERT INTO datasource_operation_attempts (org_id,effect_id,actor_id,source_id,operation,declaration_digest,request_digest) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, a.OrgID, a.EffectID, a.ActorID, a.SourceID, a.Operation, a.DeclarationDigest, a.RequestDigest)
	return tag.RowsAffected() == 1, err
}
func (s *PostgresStore) DeleteSourceOperationAttempt(ctx context.Context, org, effect string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `DELETE FROM datasource_operation_attempts WHERE org_id=$1 AND effect_id=$2`, org, effect)
	return err
}

// This is source-scoped, transaction-bound cleanup of the SDK's receipt table.
// Include maintenance receipts themselves; keep every compact attempt marker so
// neither a mutation nor an effect with changed input can be silently reused.
func (s *PostgresStore) PruneSourceOperationReceipts(ctx context.Context, org, source string, before time.Time) (int64, error) {
	tag, err := s.getQueryExecutor(ctx).Exec(ctx, `WITH expired AS (
 SELECT r.tenant,r.effect_id,r.method FROM codefly_effect_receipts r
 JOIN datasource_operation_attempts a ON a.org_id=$1::text::uuid AND a.effect_id=r.effect_id
 WHERE r.tenant=$1 AND a.source_id=$2 AND r.committed_at < $3 AND r.method IN ($4,$5)
 ORDER BY r.committed_at LIMIT 1000
)
 DELETE FROM codefly_effect_receipts r USING expired e
 WHERE r.tenant=e.tenant AND r.effect_id=e.effect_id AND r.method=e.method`, org, source, before, business.SourceOperationMethod, business.SourceReceiptRetentionMethod)
	return tag.RowsAffected(), err
}
