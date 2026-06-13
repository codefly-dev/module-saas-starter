package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"api/pkg/business"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
)

// =====================================================================
// delegation_grants — Postgres adapter
// =====================================================================
//
// Implements business.DelegationStore. Two interesting bits
// beyond plain CRUD:
//
//   - **Idempotent insert**: ON CONFLICT (org_id, request_hash)
//     returns the existing row's id. Lets agents retry
//     RequestDelegation without forking duplicate pending rows.
//
//   - **Streaming Subscribe**: uses Postgres LISTEN to react to
//     the migration's NOTIFY trigger. The channel name is
//     `delegation_grants:<uuid>`; the trigger fires on every
//     status change. Subscribe also reads the current row first
//     so a caller subscribing AFTER the decision still gets the
//     terminal event (no missed-NOTIFY race).

// =====================================================================
// Get
// =====================================================================

func (s *PostgresStore) GetDelegationGrant(ctx context.Context, id, orgID string) (*business.DelegationGrant, error) {
	w := wool.Get(ctx).In("GetDelegationGrant", wool.Field("grant_id", id))
	executor := s.getQueryExecutor(ctx)

	g, err := scanDelegationGrant(ctx, executor.QueryRow(ctx, delegationSelectByID, id, orgID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(
				fmt.Errorf("delegation grant %s not found", id),
				business.ErrTypeNotFound,
			)
		}
		return nil, w.Wrapf(err, "failed to get delegation grant")
	}
	return g, nil
}

// Get implements business.DelegationStore.
func (s *PostgresStore) Get(ctx context.Context, id, orgID string) (*business.DelegationGrant, error) {
	return s.GetDelegationGrant(ctx, id, orgID)
}

// =====================================================================
// Insert (idempotent)
// =====================================================================

func (s *PostgresStore) Insert(ctx context.Context, in *business.RequestDelegationInput, hash string, expiresAt time.Time) (string, error) {
	w := wool.Get(ctx).In("InsertDelegationGrant",
		wool.Field("actor_principal_id", in.ActorPrincipalID),
		wool.Field("action", in.Action))
	executor := s.getQueryExecutor(ctx)

	contextJSON := []byte("{}")
	if len(in.Context) > 0 {
		b, err := json.Marshal(in.Context)
		if err != nil {
			return "", w.Wrapf(err, "marshal request_context")
		}
		contextJSON = b
	}

	risk := string(in.RiskLevel)
	if risk == "" {
		risk = string(business.RiskLevelLow)
	}

	var id string
	// ON CONFLICT (org_id, request_hash) DO UPDATE SET id=id is
	// the trick to RETURNING the existing row's id when the
	// hash matches. A naked DO NOTHING wouldn't return anything;
	// DO UPDATE SET id=id is a "touch" that triggers RETURNING
	// without modifying.
	err := executor.QueryRow(ctx, delegationInsertIdempotent,
		in.OrgID, in.ActorPrincipalID, in.Action, in.Resource, in.ResourceID,
		in.Justification, contextJSON, risk, expiresAt, hash,
	).Scan(&id)
	if err != nil {
		return "", w.Wrapf(err, "insert delegation grant")
	}
	return id, nil
}

// =====================================================================
// Decide (atomic transition)
// =====================================================================

func (s *PostgresStore) Decide(ctx context.Context, id, orgID, grantorID string, status business.DelegationGrantStatus, reason string) (*business.DelegationGrant, error) {
	w := wool.Get(ctx).In("DecideDelegationGrant",
		wool.Field("grant_id", id),
		wool.Field("decision", string(status)))
	executor := s.getQueryExecutor(ctx)

	// WHERE status='pending' protects against concurrent
	// approvers stepping on each other. The first UPDATE wins;
	// the second sees zero rows-affected and returns
	// "already-decided" rather than overwriting.
	row := executor.QueryRow(ctx, delegationDecide, status, reason, grantorID, id, orgID)
	g, err := scanDelegationGrant(ctx, row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Either the grant doesn't exist, is in a different
			// org, OR was already decided by another approver.
			// Disambiguate via a follow-up read.
			existing, err2 := s.GetDelegationGrant(ctx, id, orgID)
			if err2 != nil {
				return nil, err2 // NotFound or other
			}
			if existing.IsTerminal() {
				return nil, business.NewStoreError(
					fmt.Errorf("delegation grant %s already decided (status=%s)", id, existing.Status),
					business.ErrTypeConflict,
				)
			}
			// Shouldn't happen — we matched 0 rows on pending +
			// the grant is non-terminal. Most likely cause:
			// transient connection. Return generic error.
			return nil, w.NewError("decide: 0 rows affected but grant is non-terminal (concurrent transition?)")
		}
		return nil, w.Wrapf(err, "decide")
	}
	return g, nil
}

// =====================================================================
// SetMintedToken
// =====================================================================

func (s *PostgresStore) SetMintedToken(ctx context.Context, id, orgID, tokenID string) error {
	w := wool.Get(ctx).In("SetMintedToken",
		wool.Field("grant_id", id),
		wool.Field("token_id", tokenID))
	executor := s.getQueryExecutor(ctx)

	tag, err := executor.Exec(ctx, delegationSetMintedToken, tokenID, id, orgID)
	if err != nil {
		return w.Wrapf(err, "set minted_token_id")
	}
	if tag.RowsAffected() == 0 {
		return business.NewStoreError(
			fmt.Errorf("delegation grant %s not found in org %s", id, orgID),
			business.ErrTypeNotFound,
		)
	}
	return nil
}

// =====================================================================
// ListPending
// =====================================================================

func (s *PostgresStore) ListPending(ctx context.Context, orgID string, pageSize int32, pageToken string) ([]*business.DelegationGrant, string, error) {
	w := wool.Get(ctx).In("ListPendingDelegations", wool.Field("org_id", orgID))
	executor := s.getQueryExecutor(ctx)

	args := []any{orgID, pageSize + 1}
	query := delegationListPending
	if pageToken != "" {
		// Cursor: created_at descending; pageToken is the last
		// row's (created_at,id) pair encoded. Simple form: use
		// the last id; assume created_at stable enough that
		// strict ordering by id within same-second windows is OK.
		// (Alternative: encode both and use composite cursor.)
		query = delegationListPendingCursor
		args = []any{orgID, pageToken, pageSize + 1}
	}

	rows, err := executor.Query(ctx, query, args...)
	if err != nil {
		return nil, "", w.Wrapf(err, "query pending grants")
	}
	defer rows.Close()

	grants := []*business.DelegationGrant{}
	for rows.Next() {
		g, err := scanDelegationGrantRows(ctx, rows)
		if err != nil {
			return nil, "", w.Wrapf(err, "scan grant")
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		return nil, "", w.Wrapf(err, "row iteration")
	}

	nextToken := ""
	if int32(len(grants)) > pageSize {
		nextToken = grants[pageSize-1].ID
		grants = grants[:pageSize]
	}
	return grants, nextToken, nil
}

// =====================================================================
// Subscribe — LISTEN-backed streaming
// =====================================================================

// Subscribe returns a channel that emits events on grant status
// changes. Implementation:
//
//  1. Acquire a dedicated connection from the pool (pgx
//     Acquire). LISTEN is connection-scoped; sharing pool
//     connections would cross-contaminate.
//  2. LISTEN delegation_grants:<id>.
//  3. SELECT the current row — emits the existing terminal
//     event if the grant was already decided (no missed-NOTIFY
//     race).
//  4. Loop on conn.WaitForNotification until terminal status or
//     ctx cancellation.
//  5. Release the connection on exit.
func (s *PostgresStore) Subscribe(ctx context.Context, id, orgID string) (<-chan business.DelegationDecisionEvent, error) {
	w := wool.Get(ctx).In("SubscribeDelegation",
		wool.Field("grant_id", id))

	// We use a dedicated connection from the pool because LISTEN
	// is per-connection. pgxpool.Acquire returns a *pgxpool.Conn
	// we hold for the subscription's lifetime.
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, w.Wrapf(err, "acquire connection for subscribe")
	}

	channel := "delegation_grants:" + id
	if _, err := conn.Exec(ctx, fmt.Sprintf("LISTEN %s", pgIdentifierQuote(channel))); err != nil {
		conn.Release()
		return nil, w.Wrapf(err, "LISTEN %s", channel)
	}

	out := make(chan business.DelegationDecisionEvent, 4)

	go func() {
		defer close(out)
		defer conn.Release()
		// Best-effort UNLISTEN; ignore errors (connection may
		// already be torn down).
		defer func() {
			_, _ = conn.Exec(context.Background(), fmt.Sprintf("UNLISTEN %s", pgIdentifierQuote(channel)))
		}()

		// 1. Snapshot read — catches the case where the
		//    decision happened BEFORE LISTEN started. If
		//    already-terminal, emit one event and exit.
		grant, err := scanDelegationGrant(ctx, conn.QueryRow(ctx, delegationSelectByID, id, orgID))
		if err != nil {
			// Treat as "not found"; the channel closes silently.
			// Caller saw an empty channel and can re-poll if
			// they had reason to expect the row.
			return
		}
		if grant.IsTerminal() {
			emitDelegationEvent(out, grant)
			return
		}

		// 2. Wait for NOTIFY. WaitForNotification returns when
		//    the connection receives a notification OR ctx is
		//    cancelled.
		for {
			notif, err := conn.Conn().WaitForNotification(ctx)
			if err != nil {
				// Most common: ctx cancellation. Channel
				// closes via deferred close(out).
				return
			}
			// Re-read the row to get the authoritative state.
			// The NOTIFY payload includes (id, status, ...) but
			// re-reading avoids any decoding edge cases and
			// catches the (rare) case of multiple rapid status
			// changes — we always emit the latest.
			latest, err := scanDelegationGrant(ctx, conn.QueryRow(ctx, delegationSelectByID, id, orgID))
			if err != nil {
				continue
			}
			emitDelegationEvent(out, latest)
			if latest.IsTerminal() {
				return
			}
			_ = notif
		}
	}()

	return out, nil
}

// emitDelegationEvent writes one event to the channel, dropping
// silently if the consumer has gone away.
func emitDelegationEvent(out chan<- business.DelegationDecisionEvent, g *business.DelegationGrant) {
	ev := business.DelegationDecisionEvent{
		GrantID:   g.ID,
		Status:    g.Status,
		DecidedAt: g.DecidedAt,
		Reason:    g.DecisionReason,
		GrantorID: g.GrantorPrincipalID,
		TokenSet:  g.MintedTokenID != "",
		// Token field is the encoded token. The grant row only
		// stores the token id, not the encoded form (we don't
		// want the secret-laden token in the database). The
		// RPC handler that mints fills this from its own
		// in-memory map at emit time. For now we leave Token
		// empty here; the RPC handler wraps Subscribe to add
		// the token at the streaming boundary.
		Token: "",
	}
	select {
	case out <- ev:
	default:
		// Consumer is slow; drop. With a buffered channel of
		// size 4 and at most ~1 status transition per row, a
		// drop here means the consumer is hung — let them
		// reconnect.
	}
}

// =====================================================================
// Compile-time assertion
// =====================================================================

var _ business.DelegationStore = (*PostgresStore)(nil)

// =====================================================================
// Scan helpers
// =====================================================================

// delegationGrantColumns lists the columns SELECT'd in the queries
// below, in scan order. Centralized so a future column addition
// goes in exactly two places (the column list + scanDelegationGrant).
const delegationGrantColumns = `
    id, org_id, actor_principal_id, COALESCE(grantor_principal_id::text, ''),
    action, COALESCE(resource, ''), COALESCE(resource_id, ''),
    justification, request_context, risk_level, kind, status,
    created_at, decided_at, COALESCE(decision_reason, ''), expires_at,
    COALESCE(action_pattern, ''), COALESCE(resource_pattern, ''),
    max_uses, use_count, COALESCE(minted_token_id, ''),
    COALESCE(request_hash, '')
`

func scanDelegationGrant(_ context.Context, row pgx.Row) (*business.DelegationGrant, error) {
	var g business.DelegationGrant
	var contextJSON []byte
	var maxUses *int
	var decidedAt *time.Time
	var status, kind, risk string

	err := row.Scan(
		&g.ID, &g.OrgID, &g.ActorPrincipalID, &g.GrantorPrincipalID,
		&g.Action, &g.Resource, &g.ResourceID,
		&g.Justification, &contextJSON, &risk, &kind, &status,
		&g.CreatedAt, &decidedAt, &g.DecisionReason, &g.ExpiresAt,
		&g.ActionPattern, &g.ResourcePattern,
		&maxUses, &g.UseCount, &g.MintedTokenID,
		&g.RequestHash,
	)
	if err != nil {
		return nil, err
	}

	g.Status = business.DelegationGrantStatus(status)
	g.Kind = business.DelegationGrantKind(kind)
	g.RiskLevel = business.RiskLevel(risk)
	g.MaxUses = maxUses
	g.DecidedAt = decidedAt
	if len(contextJSON) > 0 {
		_ = json.Unmarshal(contextJSON, &g.RequestContext)
	}
	return &g, nil
}

// scanDelegationGrantRows is the rows-iterator variant. Same scan
// shape; pgx separates Row (single) and Rows (iterator).
func scanDelegationGrantRows(ctx context.Context, rows pgx.Rows) (*business.DelegationGrant, error) {
	return scanDelegationGrant(ctx, rows)
}

// =====================================================================
// SQL queries
// =====================================================================

var (
	delegationSelectByID = `
        SELECT ` + delegationGrantColumns + `
        FROM delegation_grants
        WHERE id = $1 AND org_id = $2
    `

	delegationInsertIdempotent = `
        INSERT INTO delegation_grants (
            org_id, actor_principal_id, action, resource, resource_id,
            justification, request_context, risk_level, expires_at,
            request_hash, kind, status
        )
        VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''),
                $6, $7, $8, $9, $10, 'one_shot', 'pending')
        ON CONFLICT (org_id, request_hash) DO UPDATE SET id = delegation_grants.id
        RETURNING id
    `

	delegationDecide = `
        UPDATE delegation_grants
        SET status = $1, decision_reason = NULLIF($2, ''), decided_at = CURRENT_TIMESTAMP,
            grantor_principal_id = $3::uuid
        WHERE id = $4 AND org_id = $5 AND status = 'pending'
        RETURNING ` + delegationGrantColumns

	delegationSetMintedToken = `
        UPDATE delegation_grants
        SET minted_token_id = $1
        WHERE id = $2 AND org_id = $3
    `

	delegationListPending = `
        SELECT ` + delegationGrantColumns + `
        FROM delegation_grants
        WHERE org_id = $1 AND status = 'pending'
        ORDER BY
          CASE risk_level
            WHEN 'critical' THEN 0
            WHEN 'high'     THEN 1
            WHEN 'medium'   THEN 2
            ELSE 3
          END,
          created_at DESC
        LIMIT $2
    `

	delegationListPendingCursor = `
        SELECT ` + delegationGrantColumns + `
        FROM delegation_grants
        WHERE org_id = $1 AND status = 'pending' AND id < $2
        ORDER BY
          CASE risk_level
            WHEN 'critical' THEN 0
            WHEN 'high'     THEN 1
            WHEN 'medium'   THEN 2
            ELSE 3
          END,
          created_at DESC
        LIMIT $3
    `

	// delegationMatchPattern returns active pattern grants for the
	// (org, actor) tuple, newest-first. The Go side filters with
	// glob semantics; doing the glob in SQL would require a LIKE
	// per row and lose the index — pulling the small candidate
	// set and matching in code is faster and easier to reason
	// about. delegation_grants_active_pattern_idx covers the WHERE.
	delegationMatchPattern = `
        SELECT ` + delegationGrantColumns + `
        FROM delegation_grants
        WHERE org_id = $1
          AND actor_principal_id = $2
          AND kind = 'pattern'
          AND status = 'active'
          AND expires_at > CURRENT_TIMESTAMP
        ORDER BY created_at DESC
        LIMIT 50
    `

	// delegationIncrementPatternUse atomically bumps use_count.
	// The WHERE clause guards against:
	//   - revocation race (status changed away from 'active')
	//   - max_uses exhaustion (use_count >= max_uses, when bounded)
	//   - cross-org write (org_id mismatch)
	// Zero rows affected means one of those conditions tripped.
	delegationIncrementPatternUse = `
        UPDATE delegation_grants
        SET use_count = use_count + 1
        WHERE id = $1 AND org_id = $2
          AND kind = 'pattern' AND status = 'active'
          AND (max_uses IS NULL OR use_count < max_uses)
        RETURNING ` + delegationGrantColumns

	// delegationInsertAutoApproved creates a one_shot row pre-decided
	// as 'approved'. grantor_principal_id and decided_at are stamped
	// from the matched pattern. The via_pattern caveat goes into
	// request_context (jsonb) for audit; we don't have a dedicated
	// column because the schema purposefully kept request_context
	// open-ended for caveat data.
	//
	// Idempotency: ON CONFLICT (org_id, request_hash) DO UPDATE SET
	// id=id mirrors the regular Insert. A retry of an auto-approved
	// request returns the original approved row's id.
	delegationInsertAutoApproved = `
        INSERT INTO delegation_grants (
            org_id, actor_principal_id, action, resource, resource_id,
            justification, request_context, risk_level, expires_at,
            request_hash, kind, status, grantor_principal_id, decided_at
        )
        VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''),
                $6, $7, $8, $9, $10, 'one_shot', 'approved',
                $11::uuid, CURRENT_TIMESTAMP)
        ON CONFLICT (org_id, request_hash) DO UPDATE SET id = delegation_grants.id
        RETURNING id
    `
)

// =====================================================================
// MatchPattern (M8)
// =====================================================================
//
// Pulls candidate pattern grants then runs glob match in Go. Glob
// rules are exactly the manifest-ceiling shape: exact, "*", or
// "prefix*". Same predicate as adapters.globMatchAction (kept here
// as a small private function because the infra package can't
// import adapters; copying the four-line predicate is cheaper than
// a third package).

// MatchPattern implements business.DelegationStore.
func (s *PostgresStore) MatchPattern(ctx context.Context, orgID, actorID, action, resource string) (*business.DelegationGrant, error) {
	w := wool.Get(ctx).In("MatchDelegationPattern",
		wool.Field("actor", actorID),
		wool.Field("action", action))
	executor := s.getQueryExecutor(ctx)

	rows, err := executor.Query(ctx, delegationMatchPattern, orgID, actorID)
	if err != nil {
		return nil, w.Wrapf(err, "query active patterns")
	}
	defer rows.Close()

	for rows.Next() {
		g, err := scanDelegationGrantRows(ctx, rows)
		if err != nil {
			return nil, w.Wrapf(err, "scan pattern row")
		}
		if globMatchPattern(g.ActionPattern, action) && globMatchPattern(g.ResourcePattern, resource) {
			return g, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, w.Wrapf(err, "iterate pattern rows")
	}
	return nil, nil
}

// globMatchPattern matches a glob ("*", "prefix*", exact) against
// the input string. Empty pattern matches anything (the column
// allows NULL → "" via COALESCE, and a NULL pattern means
// "any value").
func globMatchPattern(pattern, s string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	if pattern == s {
		return true
	}
	if len(pattern) > 0 && pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return len(s) >= len(prefix) && s[:len(prefix)] == prefix
	}
	return false
}

// =====================================================================
// IncrementPatternUse
// =====================================================================

// IncrementPatternUse implements business.DelegationStore.
func (s *PostgresStore) IncrementPatternUse(ctx context.Context, patternID, orgID string) (*business.DelegationGrant, error) {
	w := wool.Get(ctx).In("IncrementPatternUse", wool.Field("pattern_id", patternID))
	executor := s.getQueryExecutor(ctx)

	row := executor.QueryRow(ctx, delegationIncrementPatternUse, patternID, orgID)
	g, err := scanDelegationGrant(ctx, row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Either revoked, exhausted, or wrong org. The conflict
			// type lets the business layer fall through to the
			// human-approval path without a louder error.
			return nil, business.NewStoreError(
				fmt.Errorf("pattern %s not active or use cap reached", patternID),
				business.ErrTypeConflict,
			)
		}
		return nil, w.Wrapf(err, "increment pattern use")
	}
	return g, nil
}

// =====================================================================
// InsertAutoApproved (M8)
// =====================================================================

// InsertAutoApproved implements business.DelegationStore.
func (s *PostgresStore) InsertAutoApproved(ctx context.Context, in *business.RequestDelegationInput, hash string, expiresAt time.Time, viaPattern *business.DelegationGrant) (string, error) {
	w := wool.Get(ctx).In("InsertAutoApprovedDelegation",
		wool.Field("actor_principal_id", in.ActorPrincipalID),
		wool.Field("via_pattern", viaPattern.ID))
	executor := s.getQueryExecutor(ctx)

	// Build the request_context JSON with the via_pattern caveat
	// merged in. Auditors reading the row see exactly which
	// pattern auto-approved this — same rationale as the
	// principal_rpcs.go via_approval caveat.
	caveatedContext := map[string]any{}
	for k, v := range in.Context {
		caveatedContext[k] = v
	}
	caveatedContext["via_pattern"] = viaPattern.ID
	caveatedContext["pattern_grantor"] = viaPattern.GrantorPrincipalID
	contextJSON, err := json.Marshal(caveatedContext)
	if err != nil {
		return "", w.Wrapf(err, "marshal request_context with via_pattern caveat")
	}

	risk := string(in.RiskLevel)
	if risk == "" {
		risk = string(business.RiskLevelLow)
	}

	var id string
	err = executor.QueryRow(ctx, delegationInsertAutoApproved,
		in.OrgID, in.ActorPrincipalID, in.Action, in.Resource, in.ResourceID,
		in.Justification, contextJSON, risk, expiresAt, hash,
		viaPattern.GrantorPrincipalID,
	).Scan(&id)
	if err != nil {
		return "", w.Wrapf(err, "insert auto-approved delegation")
	}
	return id, nil
}

// pgIdentifierQuote double-quotes a Postgres identifier and
// escapes embedded double quotes. Used for LISTEN/UNLISTEN
// channel names which contain ':' (not allowed in unquoted
// identifiers).
//
// We construct the channel name from controlled inputs (the
// grant uuid), so injection risk is bounded — but defense at
// the boundary is cheap.
func pgIdentifierQuote(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			out = append(out, '"', '"')
			continue
		}
		out = append(out, s[i])
	}
	out = append(out, '"')
	return string(out)
}
