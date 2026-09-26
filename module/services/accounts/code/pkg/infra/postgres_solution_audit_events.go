package infra

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// Solution-declared audit event types are rows of audit_event_types whose owner
// starts with business.SolutionAuditOwnerPrefix. audit_event_types is a global
// relation only app_control_plane may write, so every method here assumes the
// caller opened a WithControlPlane transaction, which getQueryExecutor picks up
// from ctx.

// auditNamespaceLockKey scopes the advisory lock to audit namespaces, apart
// from every other advisory key the store takes.
func auditNamespaceLockKey(namespace string) string {
	return "audit-event-namespace/" + namespace
}

// LockAuditEventNamespace holds a transaction-scoped advisory lock on one
// namespace. Two admissions into an unowned namespace would otherwise both read
// it as unowned and both claim it.
func (s *PostgresStore) LockAuditEventNamespace(ctx context.Context, namespace string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, auditNamespaceLockKey(namespace))
	return err
}

// ListAuditEventNamespaceOwners returns every distinct owner of a type in the
// namespace, deprecated types included: a deprecated type still has historical
// rows, and its namespace is still its owner's.
func (s *PostgresStore) ListAuditEventNamespaceOwners(ctx context.Context, namespace string) ([]string, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx,
		`SELECT DISTINCT owner FROM audit_event_types WHERE namespace = $1 ORDER BY owner`, namespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var owners []string
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			return nil, err
		}
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}

func scanDeclaredAuditEventType(row pgx.Row) (*business.DeclaredAuditEventType, error) {
	var (
		name, namespace, owner string
		schema                 []byte
	)
	if err := row.Scan(&name, &namespace, &owner, &schema); err != nil {
		return nil, err
	}
	declared, err := business.DeclaredAuditEventTypeFromSchema(business.EventType(name), namespace, owner, schema)
	if err != nil {
		return nil, err
	}
	return &declared, nil
}

// GetDeclaredAuditEventType reads one solution-declared type, or nil when the
// type is absent or is code-owned.
//
// It is the declared half of the one audit type lookup
// (business.AuditEventResolver), which runs both inside a transaction — the
// audit write, admission — and outside one — the export job, a download. On a
// transaction it reads there (app_tenant may SELECT the table, and a read on
// the caller's transaction sees what that transaction admitted); with none it
// opens a control-plane transaction of its own rather than reading as whatever
// role the pool logs in as.
func (s *PostgresStore) GetDeclaredAuditEventType(ctx context.Context, eventType business.EventType) (*business.DeclaredAuditEventType, error) {
	if _, inTx := ctx.Value("tx").(pgx.Tx); !inTx {
		var declared *business.DeclaredAuditEventType
		err := s.withControlPlaneTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context) error {
			var err error
			declared, err = s.GetDeclaredAuditEventType(ctx, eventType)
			return err
		})
		return declared, err
	}
	declared, err := scanDeclaredAuditEventType(s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT name, namespace, owner, payload_schema FROM audit_event_types
		 WHERE name = $1 AND starts_with(owner, $2)`,
		string(eventType), business.SolutionAuditOwnerPrefix))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return declared, err
}

// ListDeclaredAuditEventTypes returns every solution-declared type, sorted by
// type.
func (s *PostgresStore) ListDeclaredAuditEventTypes(ctx context.Context) ([]business.DeclaredAuditEventType, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx,
		`SELECT name, namespace, owner, payload_schema FROM audit_event_types
		 WHERE starts_with(owner, $1) ORDER BY name`, business.SolutionAuditOwnerPrefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []business.DeclaredAuditEventType
	for rows.Next() {
		declared, err := scanDeclaredAuditEventType(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *declared)
	}
	return out, rows.Err()
}

// PutDeclaredAuditEventType inserts a declared type or replaces the one its
// owner already holds. The conflict update is conditioned on the owner, so a
// row another producer holds is left alone and the write is refused rather
// than silently transferring the type.
func (s *PostgresStore) PutDeclaredAuditEventType(ctx context.Context, declared business.DeclaredAuditEventType) error {
	tag, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO audit_event_types (name, namespace, version, category, owner, payload_schema, deprecated, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, FALSE, NOW())
		ON CONFLICT (name) DO UPDATE SET
			payload_schema = EXCLUDED.payload_schema,
			deprecated = FALSE,
			updated_at = NOW()
		WHERE audit_event_types.owner = EXCLUDED.owner`,
		string(declared.Type), declared.Namespace, business.DeclaredAuditEventVersion,
		string(business.CategorySolution), business.SolutionAuditOwner(declared.SolutionID),
		declared.PayloadSchemaJSON())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: %w: event type %q is held by another owner",
			business.ErrSolutionAuditDeclarationRejected, business.ErrSolutionAuditNamespaceOwned, declared.Type)
	}
	return nil
}

// TransferAuditEventNamespace moves every type in a namespace from one owner to
// another. Admission calls it, under the namespace's advisory lock, only once
// the operator has unbound the namespace from the holding solution and bound it
// to the admitting one; the rows keep their schemas, so the additive rule keeps
// governing what was already written under them.
func (s *PostgresStore) TransferAuditEventNamespace(ctx context.Context, namespace, from, to string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx,
		`UPDATE audit_event_types SET owner = $3, updated_at = NOW() WHERE namespace = $1 AND owner = $2`,
		namespace, from, to)
	return err
}
