package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
)

// GetPrincipal returns the principal row by ID. Returns
// business.NewStoreError(ErrTypeNotFound) when the row doesn't exist
// (caller distinguishes "missing" from "broken DB" by error type).
//
// Revoked principals are RETURNED — IsRevoked() is the discriminator.
// The business layer applies the "revoked = invisible to fresh-action
// callers" policy; the storage layer is faithful to the row.
// principalColumns is the canonical SELECT list for a principals row, in the
// order scanPrincipal expects. ListPrincipals' human/UNION branches alias the
// table as `p`, so they inline a `p.`-prefixed copy of this same order.
const principalColumns = `id, kind, display_name, org_id, agent_identifier,
	created_at, revoked_at, revoked_reason, created_by,
	allowed_audiences, allowed_scopes, disabled_at, disabled_reason`

// principalColumnsPrefixed is principalColumns with a `p.` table alias, for the
// ListPrincipals human/UNION branches that join principals AS p.
const principalColumnsPrefixed = `p.id, p.kind, p.display_name, p.org_id, p.agent_identifier,
	p.created_at, p.revoked_at, p.revoked_reason, p.created_by,
	p.allowed_audiences, p.allowed_scopes, p.disabled_at, p.disabled_reason`

// stringArray normalizes a nil ceiling slice to a non-nil empty slice so it
// encodes to the SQL empty array '{}' rather than NULL, matching the schema's
// NOT NULL TEXT[] columns (org_identity_providers.allowed_email_domains does the
// same). Empty and nil both mean "unrestricted".
func stringArray(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// rowScanner is satisfied by both pgx.Row (QueryRow) and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanPrincipal reads one principals row selected via principalColumns into a
// business.Principal, translating the nullable columns.
func scanPrincipal(row rowScanner) (*business.Principal, error) {
	var p business.Principal
	var orgID, agentID, revokedReason, createdBy, disabledReason *string
	var revokedAt, disabledAt *time.Time
	var allowedAudiences, allowedScopes []string
	if err := row.Scan(&p.ID, &p.Kind, &p.DisplayName, &orgID, &agentID,
		&p.CreatedAt, &revokedAt, &revokedReason, &createdBy,
		&allowedAudiences, &allowedScopes, &disabledAt, &disabledReason); err != nil {
		return nil, err
	}
	if orgID != nil {
		p.OrgID = *orgID
	}
	if agentID != nil {
		p.AgentIdentifier = *agentID
	}
	if revokedAt != nil {
		t := *revokedAt
		p.RevokedAt = &t
	}
	if revokedReason != nil {
		p.RevokedReason = *revokedReason
	}
	if createdBy != nil {
		p.CreatedBy = *createdBy
	}
	p.AllowedAudiences = allowedAudiences
	p.AllowedScopes = allowedScopes
	if disabledAt != nil {
		t := *disabledAt
		p.DisabledAt = &t
	}
	if disabledReason != nil {
		p.DisabledReason = *disabledReason
	}
	return &p, nil
}

func (s *PostgresStore) GetPrincipal(ctx context.Context, id string) (*business.Principal, error) {
	w := wool.Get(ctx).In("GetPrincipal", wool.Field("principal_id", id))
	executor := s.getQueryExecutor(ctx)

	p, err := scanPrincipal(executor.QueryRow(ctx,
		`SELECT `+principalColumns+` FROM principals WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(
				fmt.Errorf("principal %s not found", id),
				business.ErrTypeNotFound,
			)
		}
		return nil, w.Wrapf(err, "failed to get principal")
	}
	return p, nil
}

// GetAgentPrincipal looks up an agent principal by its canonical
// identifier within an org. Same NotFound semantics as GetPrincipal.
//
// We rely on the partial unique index `principals_agent_identifier_org_idx`
// (kind='agent' AND revoked_at IS NULL) for at-most-one matching row.
// Revoked agents are explicitly NOT returned here — their canonical
// slot is "free" for re-installation.
func (s *PostgresStore) GetAgentPrincipal(ctx context.Context, orgID, agentIdentifier string) (*business.Principal, error) {
	w := wool.Get(ctx).In("GetAgentPrincipal",
		wool.Field("org_id", orgID),
		wool.Field("agent_id", agentIdentifier))
	executor := s.getQueryExecutor(ctx)

	p, err := scanPrincipal(executor.QueryRow(ctx, `
		SELECT `+principalColumns+`
		FROM principals
		WHERE kind = 'agent'
		  AND org_id = $1
		  AND agent_identifier = $2
		  AND revoked_at IS NULL`,
		orgID, agentIdentifier,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(
				fmt.Errorf("agent %s not found in org %s", agentIdentifier, orgID),
				business.ErrTypeNotFound,
			)
		}
		return nil, w.Wrapf(err, "failed to get agent principal")
	}
	return p, nil
}

// CreateAgentPrincipal inserts a new agent row. Returns
// business.NewStoreError(ErrTypeConflict) on UNIQUE-violation
// (someone raced us between the business-layer idempotency check and
// this INSERT). The CHECK constraints (principals_kind_valid,
// principals_org_scope, principals_agent_identifier_consistency) are
// validated by the SQL layer; business.Principal.Validate() catches
// most issues earlier with friendlier errors.
func (s *PostgresStore) CreateAgentPrincipal(ctx context.Context, p *business.Principal) error {
	w := wool.Get(ctx).In("CreateAgentPrincipal",
		wool.Field("principal_id", p.ID),
		wool.Field("agent_id", p.AgentIdentifier))
	executor := s.getQueryExecutor(ctx)

	if p.Kind != business.PrincipalKindAgent {
		return w.NewError("CreateAgentPrincipal called with kind=%q (want 'agent')", p.Kind)
	}

	_, err := executor.Exec(ctx, `
		INSERT INTO principals (id, kind, display_name, org_id, agent_identifier, created_by, created_at,
		                        allowed_audiences, allowed_scopes)
		VALUES ($1, 'agent', $2, $3, $4, NULLIF($5, '')::uuid, $6, $7, $8)`,
		p.ID, p.DisplayName, p.OrgID, p.AgentIdentifier, p.CreatedBy, p.CreatedAt,
		stringArray(p.AllowedAudiences), stringArray(p.AllowedScopes),
	)
	if err != nil {
		// pgx UNIQUE-violation surfaces with SQLSTATE 23505. We don't
		// import pgconn just for this — the message contains
		// "duplicate key" reliably across pg versions.
		if isUniqueViolation(err) {
			return business.NewStoreError(
				fmt.Errorf("agent %s already registered in org %s", p.AgentIdentifier, p.OrgID),
				business.ErrTypeConflict,
			)
		}
		return w.Wrapf(err, "failed to insert agent principal")
	}
	return nil
}

// RevokePrincipal marks the principal revoked. Idempotent:
//
//   - First call: sets revoked_at and revoked_reason.
//   - Second call (already revoked): no-op, returns nil. The
//     original revoked_at / reason are preserved — a later "revoke
//     for a different reason" doesn't overwrite the audit trail.
//
// Returns ErrTypeNotFound if no row matches the id.
func (s *PostgresStore) RevokePrincipal(ctx context.Context, id, reason string) error {
	w := wool.Get(ctx).In("RevokePrincipal", wool.Field("principal_id", id))
	executor := s.getQueryExecutor(ctx)

	// WHERE revoked_at IS NULL means this only fires on a fresh
	// revocation; double-revocations leave the original alone.
	tag, err := executor.Exec(ctx, `
		UPDATE principals
		SET revoked_at = CURRENT_TIMESTAMP, revoked_reason = $1
		WHERE id = $2 AND revoked_at IS NULL`,
		reason, id,
	)
	if err != nil {
		return w.Wrapf(err, "failed to revoke principal")
	}

	if tag.RowsAffected() == 0 {
		// Two cases: principal doesn't exist, OR principal already
		// revoked. Disambiguate with a follow-up read so callers get
		// a clear NotFound vs the silent already-revoked happy path.
		var exists bool
		if err := executor.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM principals WHERE id = $1)`, id).
			Scan(&exists); err != nil {
			return w.Wrapf(err, "failed to verify principal existence after revoke no-op")
		}
		if !exists {
			return business.NewStoreError(
				fmt.Errorf("principal %s not found", id),
				business.ErrTypeNotFound,
			)
		}
		// Already revoked — idempotent success.
		w.Trace("principal already revoked; idempotent no-op")
	}
	return nil
}

// DisableAgentPrincipal sets disabled_at/disabled_reason on an agent row.
// Idempotent: `WHERE disabled_at IS NULL` means a second disable leaves the
// original timestamp/reason intact. Returns ErrTypeNotFound when no agent row
// matches the id (a non-agent id simply matches nothing here — the business
// layer has already rejected that case with a clearer error).
func (s *PostgresStore) DisableAgentPrincipal(ctx context.Context, id, reason string) error {
	w := wool.Get(ctx).In("DisableAgentPrincipal", wool.Field("principal_id", id))
	executor := s.getQueryExecutor(ctx)

	tag, err := executor.Exec(ctx, `
		UPDATE principals
		SET disabled_at = CURRENT_TIMESTAMP, disabled_reason = $1
		WHERE id = $2 AND kind = 'agent' AND disabled_at IS NULL`,
		reason, id,
	)
	if err != nil {
		return w.Wrapf(err, "failed to disable agent principal")
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := executor.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM principals WHERE id = $1 AND kind = 'agent')`, id).
			Scan(&exists); err != nil {
			return w.Wrapf(err, "failed to verify agent existence after disable no-op")
		}
		if !exists {
			return business.NewStoreError(
				fmt.Errorf("agent principal %s not found", id),
				business.ErrTypeNotFound,
			)
		}
		w.Trace("agent principal already disabled; idempotent no-op")
	}
	return nil
}

// EnableAgentPrincipal clears disabled_at/disabled_reason. Idempotent on an
// already-active agent (`WHERE disabled_at IS NOT NULL`). Returns
// ErrTypeNotFound when no agent row matches the id.
func (s *PostgresStore) EnableAgentPrincipal(ctx context.Context, id string) error {
	w := wool.Get(ctx).In("EnableAgentPrincipal", wool.Field("principal_id", id))
	executor := s.getQueryExecutor(ctx)

	tag, err := executor.Exec(ctx, `
		UPDATE principals
		SET disabled_at = NULL, disabled_reason = NULL
		WHERE id = $1 AND kind = 'agent' AND disabled_at IS NOT NULL`,
		id,
	)
	if err != nil {
		return w.Wrapf(err, "failed to enable agent principal")
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := executor.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM principals WHERE id = $1 AND kind = 'agent')`, id).
			Scan(&exists); err != nil {
			return w.Wrapf(err, "failed to verify agent existence after enable no-op")
		}
		if !exists {
			return business.NewStoreError(
				fmt.Errorf("agent principal %s not found", id),
				business.ErrTypeNotFound,
			)
		}
		w.Trace("agent principal already active; idempotent no-op")
	}
	return nil
}

// ListPrincipals returns paginated principals in the org, optionally
// filtered by kind. The empty kind matches all kinds.
//
// Pagination is cursor-based via the principal id (lexicographic):
// pageToken is the last id from the previous page. NULL token =
// first page. Stable order by created_at DESC, id DESC for tie-break
// reproducibility.
//
// **Why we don't filter by revoked_at here.** The list is for admin
// UI which needs to show both active AND revoked principals (so the
// user can see what's been revoked, when, and why). The kind=human
// special case (org_id IS NULL) is filtered by joining with
// organization_members to scope to the requesting org.
func (s *PostgresStore) ListPrincipals(ctx context.Context, orgID, kind string, pageSize int32, pageToken string) ([]*business.Principal, string, error) {
	w := wool.Get(ctx).In("ListPrincipals",
		wool.Field("org_id", orgID),
		wool.Field("kind", kind))
	executor := s.getQueryExecutor(ctx)

	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}

	// Two query shapes: humans (cross-org via organization_members
	// join) vs services/agents (direct org_id match).
	args := []any{}
	var query string
	switch kind {
	case business.PrincipalKindHuman:
		query = `
			SELECT ` + principalColumnsPrefixed + `
			FROM principals p
			JOIN organization_members om ON om.user_id = p.id
			WHERE p.kind = 'human' AND om.org_id = $1`
		args = append(args, orgID)
	case business.PrincipalKindService, business.PrincipalKindAgent:
		query = `
			SELECT ` + principalColumns + `
			FROM principals
			WHERE kind = $1 AND org_id = $2`
		args = append(args, kind, orgID)
	case "":
		// All kinds — UNION humans (cross-org via membership) with
		// services/agents (direct org_id). The UNION shape keeps the
		// query coherent even though humans and the others use
		// different join paths.
		query = `
			(SELECT ` + principalColumnsPrefixed + `
			 FROM principals p
			 JOIN organization_members om ON om.user_id = p.id
			 WHERE p.kind = 'human' AND om.org_id = $1)
			UNION ALL
			(SELECT ` + principalColumns + `
			 FROM principals
			 WHERE kind IN ('service', 'agent') AND org_id = $1)`
		args = append(args, orgID)
	default:
		return nil, "", w.NewError("unknown kind %q", kind)
	}

	// Wrap the base query in a subquery before applying the cursor / ORDER BY /
	// LIMIT. For the all-kinds case the base query is a `(...) UNION ALL (...)`
	// expression: appending `AND id < $N` directly to that is invalid SQL (it
	// lands after the second branch's closing paren), so paging past page 1
	// failed. Wrapping makes the cursor apply to the union result for every kind.
	query = `SELECT ` + principalColumns + ` FROM (` + query + `) AS principals`
	if pageToken != "" {
		query += fmt.Sprintf(` WHERE id < $%d`, len(args)+1)
		args = append(args, pageToken)
	}

	query += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args)+1)
	args = append(args, pageSize+1)

	rows, err := executor.Query(ctx, query, args...)
	if err != nil {
		return nil, "", w.Wrapf(err, "failed to list principals")
	}
	defer rows.Close()

	var principals []*business.Principal
	for rows.Next() {
		p, err := scanPrincipal(rows)
		if err != nil {
			return nil, "", w.Wrapf(err, "failed to scan principal")
		}
		principals = append(principals, p)
	}
	if err := rows.Err(); err != nil {
		return nil, "", w.Wrapf(err, "row scan error")
	}

	nextToken := ""
	if int32(len(principals)) > pageSize {
		principals = principals[:pageSize]
		nextToken = principals[len(principals)-1].ID
	}
	return principals, nextToken, nil
}

// isUniqueViolation reports whether err is a Postgres unique-key
// violation (SQLSTATE 23505). We check the error string rather than
// importing pgconn for SQLSTATE introspection — the import would
// pull a transitive surface that's heavier than the cost of a string
// match for a single constant.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "duplicate key") || contains(msg, "23505")
}

// contains is strings.Contains without the import — kept private so
// it doesn't accidentally become a general utility.
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// --- Compile-time interface assertion ------------------------------

// Compile-time check that PostgresStore satisfies the PrincipalStore
// interface declared in business/principals.go. Drift surfaces here
// as a build error, not at first call site.
var _ business.PrincipalStore = (*PostgresStore)(nil)
