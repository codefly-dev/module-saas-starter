// Package business — delegation_grants: M7 escalation flow + M8
// pattern grants.
//
// **Why this file exists.** Migration 38 added the
// delegation_grants table for two purposes:
//
//   - M7 (synchronous escalation): one_shot grants. An actor
//     requests authority; a grantor approves or denies. Each row
//     is one request-decision pair.
//   - M8 (pattern grants): pre-authorized class of future calls.
//     A grantor configures a pattern; future requests matching
//     the pattern auto-issue without per-call human approval.
//
// This file is the business-layer surface the codefly host
// (Mind / CLI / gateway) calls into via the M7 RPCs:
// RequestDelegation, WaitForDelegation, DecideDelegation,
// ListPendingDelegations.
//
// The streaming WaitForDelegation reads NOTIFY events from
// Postgres (the migration installs a trigger that fires
// pg_notify on every status change). The infra layer
// (postgres_delegation_grants.go) handles LISTEN.
package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"
)

// =====================================================================
// Types
// =====================================================================

// DelegationGrantStatus mirrors the SQL CHECK enum. Renaming
// values requires both the migration and this file to change in
// lock-step.
type DelegationGrantStatus string

const (
	GrantStatusPending   DelegationGrantStatus = "pending"
	GrantStatusApproved  DelegationGrantStatus = "approved"
	GrantStatusDenied    DelegationGrantStatus = "denied"
	GrantStatusExpired   DelegationGrantStatus = "expired"
	GrantStatusCancelled DelegationGrantStatus = "cancelled"
	GrantStatusActive    DelegationGrantStatus = "active" // pattern-grant only
)

// DelegationGrantKind discriminates one_shot (M7) from pattern
// (M8). The same table holds both shapes; status semantics
// differ slightly (one_shot transitions through pending; pattern
// is created as 'active').
type DelegationGrantKind string

const (
	GrantKindOneShot DelegationGrantKind = "one_shot"
	GrantKindPattern DelegationGrantKind = "pattern"
)

// RiskLevel mirrors the manifest's risk_levels. Free string so
// future tiers (e.g. "extreme") can be added without re-coding;
// validation is done at insert time.
type RiskLevel string

const (
	RiskLevelLow      RiskLevel = "low"
	RiskLevelMedium   RiskLevel = "medium"
	RiskLevelHigh     RiskLevel = "high"
	RiskLevelCritical RiskLevel = "critical"
)

// DelegationGrant mirrors the delegation_grants row 1:1.
type DelegationGrant struct {
	ID                 string
	OrgID              string
	ActorPrincipalID   string
	GrantorPrincipalID string // nullable; empty until decided
	Action             string
	Resource           string
	ResourceID         string
	Justification      string
	RequestContext     map[string]any
	RiskLevel          RiskLevel
	Kind               DelegationGrantKind
	Status             DelegationGrantStatus
	CreatedAt          time.Time
	DecidedAt          *time.Time
	DecisionReason     string
	ExpiresAt          time.Time
	ActionPattern      string // pattern grants only
	ResourcePattern    string // pattern grants only
	MaxUses            *int
	UseCount           int
	MintedTokenID      string
	RequestHash        string
}

// IsTerminal reports whether the grant has reached a final
// state (no further status transitions possible). Used by
// WaitForDelegation to decide when to close the stream.
func (g *DelegationGrant) IsTerminal() bool {
	switch g.Status {
	case GrantStatusApproved, GrantStatusDenied, GrantStatusExpired, GrantStatusCancelled:
		return true
	}
	return false
}

// =====================================================================
// Inputs
// =====================================================================

// RequestDelegationInput is the business-layer input shape.
// RPC adapters convert from the gRPC type to this.
type RequestDelegationInput struct {
	OrgID            string
	ActorPrincipalID string
	Action           string
	Resource         string
	ResourceID       string
	Justification    string
	Context          map[string]any
	RiskLevel        RiskLevel
	Timeout          time.Duration
	// Grantor optionally targets a specific approver ("user:<uuid>")
	// or role ("team:engineering"). Empty → default approver chain.
	Grantor string
	// RequestHash is the idempotency key. Empty → server derives.
	RequestHash string
}

// Validate checks the input is structurally complete. Called
// before the database insert so misconfigured calls fail without
// a round-trip.
func (in *RequestDelegationInput) Validate() error {
	if in == nil {
		return errors.New("delegation: nil input")
	}
	if in.OrgID == "" {
		return errors.New("delegation: org_id required")
	}
	if in.ActorPrincipalID == "" {
		return errors.New("delegation: actor_principal_id required")
	}
	if in.Action == "" {
		return errors.New("delegation: action required")
	}
	if strings.TrimSpace(in.Justification) == "" {
		// Hard requirement — empty justifications produce empty
		// approval prompts and turn approval into rubber-stamping.
		return errors.New("delegation: justification required (the grantor needs to know why)")
	}
	if in.Timeout <= 0 {
		// Without a timeout, requests can pile up forever.
		// 5 minutes is the default at the SDK side; require
		// explicit non-zero here for safety.
		return errors.New("delegation: timeout must be > 0")
	}
	switch in.RiskLevel {
	case "", RiskLevelLow, RiskLevelMedium, RiskLevelHigh, RiskLevelCritical:
		// ok; "" defaults to low at insert time
	default:
		return fmt.Errorf("delegation: risk_level %q must be low|medium|high|critical", in.RiskLevel)
	}
	return nil
}

// ComputeRequestHash deterministically hashes the request
// fields that define identity. Two requests with the same hash
// are considered the same request — the table's UNIQUE index
// dedupes them, returning the original row to the caller.
//
// Why deterministic: an agent retrying the same RequestDelegation
// (e.g. after a transient network error) shouldn't create a
// second pending row. The grantor sees one pending entry; both
// retries observe the same status transitions.
//
// Why these fields: org + actor + action + resource + justification.
// Two genuinely-different requests can differ on any of these
// (different action / resource / different reasoning). Two
// retries of the same logical request match on all five.
//
// Excluded: timestamp (retries within the same second are
// dedup'd by design), context (snapshot data should not change
// the identity), grantor (different grantors for the same
// request shouldn't fork into two rows — the routing is a
// concern of the approval system, not the request identity).
func ComputeRequestHash(in *RequestDelegationInput) string {
	h := sha256.New()
	h.Write([]byte(in.OrgID))
	h.Write([]byte{0})
	h.Write([]byte(in.ActorPrincipalID))
	h.Write([]byte{0})
	h.Write([]byte(in.Action))
	h.Write([]byte{0})
	h.Write([]byte(in.Resource))
	h.Write([]byte{0})
	h.Write([]byte(strings.TrimSpace(in.Justification)))
	return hex.EncodeToString(h.Sum(nil))
}

// =====================================================================
// Decision events (NOTIFY payload shape)
// =====================================================================

// DelegationDecisionEvent is the structured payload the
// streaming RPC delivers. Mirrors the JSON the SQL trigger
// builds in pg_notify.
type DelegationDecisionEvent struct {
	GrantID   string
	Status    DelegationGrantStatus
	DecidedAt *time.Time
	Reason    string
	GrantorID string
	// TokenSet is true when status=approved AND the server has
	// minted + persisted the scoped-auth token. The streaming
	// RPC delays sending the event until the mint completes
	// (otherwise the agent could see status=approved but
	// minted_token_id empty). Tracked here so callers can
	// distinguish "approved, no token yet (mint failed)" from
	// "approved + token ready".
	TokenSet bool
	// Token is the encoded scoped-auth string. Empty unless
	// status=approved AND TokenSet.
	Token string
}

// =====================================================================
// Store interface
// =====================================================================

// DelegationStore is the persistence surface. Defined here so
// the business layer depends on a narrow interface rather than
// PostgresStore directly — easier to test, cleaner package
// boundaries.
type DelegationStore interface {
	// Insert creates a new grant row. ON CONFLICT (org_id,
	// request_hash) DO NOTHING — returns the existing row's id
	// when a duplicate hash is supplied.
	Insert(ctx context.Context, in *RequestDelegationInput, hash string, expiresAt time.Time) (string, error)

	// Get returns one grant by id. Returns ErrTypeNotFound when
	// the row doesn't exist OR is in a different org than the
	// caller's (cross-org reads are blocked by RLS upstream).
	Get(ctx context.Context, id, orgID string) (*DelegationGrant, error)

	// Decide atomically transitions a pending grant to
	// approved/denied. Honors the WHERE status='pending'
	// guard so concurrent approvers can't collide. Returns the
	// updated row.
	Decide(ctx context.Context, id, orgID, grantorID string, status DelegationGrantStatus, reason string) (*DelegationGrant, error)

	// SetMintedToken records the scoped-auth token id after
	// mint completes. Called by the DecideDelegation handler
	// AFTER the atomic Decide so the token field is populated
	// before WaitForDelegation streams the event.
	SetMintedToken(ctx context.Context, id, orgID, tokenID string) error

	// ListPending returns paginated pending grants for the
	// approver UI. Sorted by risk_level DESC then created_at DESC.
	ListPending(ctx context.Context, orgID string, pageSize int32, pageToken string) ([]*DelegationGrant, string, error)

	// Subscribe returns a channel of decision events for the
	// given grant id. The channel closes when the grant reaches
	// a terminal status OR the context is cancelled. Backed by
	// Postgres LISTEN; no polling.
	Subscribe(ctx context.Context, id, orgID string) (<-chan DelegationDecisionEvent, error)

	// MatchPattern returns the most-recently-created active
	// pattern grant whose (action_pattern, resource_pattern)
	// matches (action, resource) for this actor in this org.
	// Nil + nil-error means "no matching pattern".
	//
	// Glob semantics: exact match, or trailing "*" prefix match,
	// applied independently to action and resource.
	MatchPattern(ctx context.Context, orgID, actorID, action, resource string) (*DelegationGrant, error)

	// IncrementPatternUse atomically bumps use_count and returns
	// the updated row. Fails with ErrTypeConflict when max_uses
	// is exhausted (use_count >= max_uses) or status is no longer
	// active. Idempotency-safe under concurrent auto-approves: the
	// UPDATE is conditional on status='active' AND use_count <
	// max_uses (or max_uses NULL), so a losing racer sees zero
	// rows affected and gets the conflict.
	IncrementPatternUse(ctx context.Context, patternID, orgID string) (*DelegationGrant, error)

	// InsertAutoApproved creates a one_shot grant pre-decided as
	// 'approved' because a pattern grant matched. The grantor and
	// via_pattern caveat are persisted in request_context for
	// audit (auditable: "this row was approved by pattern X, not
	// by the grantor logging in"). decided_at is set to NOW().
	//
	// Returns the inserted row's id. Honors the same idempotency
	// hash as Insert — a retried request with the same hash
	// returns the original row without creating a duplicate.
	InsertAutoApproved(ctx context.Context, in *RequestDelegationInput, hash string, expiresAt time.Time, viaPattern *DelegationGrant) (string, error)
}

// =====================================================================
// Service methods
// =====================================================================

// RequestDelegation creates a pending grant. Idempotent on
// (org_id, request_hash) — same request from a retrying actor
// returns the original row's id without a second insert.
//
// **What it does NOT do.** Notification fan-out (Slack, email,
// in-app inbox) — that's a separate concern handled by a
// listener over the same NOTIFY channel OR by an outbox pattern
// reading audit_events. Keeping this method focused on the
// canonical write keeps it idempotent.
func (s *Service) RequestDelegation(ctx context.Context, in *RequestDelegationInput) (string, error) {
	w := wool.Get(ctx).In("RequestDelegation",
		wool.Field("actor_principal_id", in.ActorPrincipalID),
		wool.Field("action", in.Action))
	if err := in.Validate(); err != nil {
		return "", w.Wrapf(err, "validate")
	}

	// Compute idempotency hash if caller didn't supply one.
	// Server-derived hash is deterministic + uses fields that
	// matter for identity.
	hash := in.RequestHash
	if hash == "" {
		hash = ComputeRequestHash(in)
	}

	expiresAt := time.Now().UTC().Add(in.Timeout)

	// M8 pattern-grant fast path.
	//
	// Scan for an active pattern grant whose (action_pattern,
	// resource_pattern) matches this request. If one's found,
	// atomically increment its use_count and insert the request
	// row pre-decided as 'approved' with a via_pattern caveat.
	// This skips the human-in-the-loop step for already-trusted
	// classes of operations (e.g. "any auto-merge for repo:*
	// for the next hour, max 100 uses").
	//
	// Failure modes are deliberately fall-through, not fatal:
	//
	//   - MatchPattern errors → log + continue to one_shot path.
	//     Pattern matching is an optimization; the human-approval
	//     fallback always works.
	//   - IncrementPatternUse fails (max_uses exhausted, race
	//     loss) → continue to one_shot path. The actor still
	//     gets approval; just synchronously through a human.
	//
	// We MUST NOT short-circuit insert errors from the auto-
	// approved path (those are real DB issues; they'd also fail
	// the one_shot path, so surface them).
	// delegation_grants is RLS-protected (org-scoped); run each store call as the
	// grant's org. Narrow wraps (not the notify path, which is user-scoped and
	// has its own WithUserTx — wrapping it here would nest).
	scoped := s.store.As(Identity{OrgID: in.OrgID})
	var pattern *DelegationGrant
	patErr := scoped.Within(ctx, func(ctx context.Context) error {
		var e error
		pattern, e = s.delegationStore().MatchPattern(ctx, in.OrgID, in.ActorPrincipalID, in.Action, in.Resource)
		return e
	})
	if patErr == nil && pattern != nil {
		var incErr error
		_ = scoped.Within(ctx, func(ctx context.Context) error {
			_, incErr = s.delegationStore().IncrementPatternUse(ctx, pattern.ID, in.OrgID)
			return incErr
		})
		if incErr == nil {
			var id string
			insErr := scoped.Within(ctx, func(ctx context.Context) error {
				var e error
				id, e = s.delegationStore().InsertAutoApproved(ctx, in, hash, expiresAt, pattern)
				return e
			})
			if insErr != nil {
				return "", w.Wrapf(insErr, "insert auto-approved (via pattern)")
			}
			w.Info("delegation auto-approved via pattern",
				wool.Field("grant_id", id),
				wool.Field("pattern_id", pattern.ID))
			s.emit(ctx, in.ActorPrincipalID, "agent",
				"delegation.auto_approved", "delegation_grant", id, in.OrgID)
			// Notify the actor's owning user (when the actor is a
			// human; agent actors silently skip — no inbox to
			// post to). Best-effort: failure here doesn't roll
			// back the grant.
			s.notifyDelegationDecision(ctx, in.ActorPrincipalID, in.OrgID, id, "approved",
				"Auto-approved via pattern grant", in.Action)
			return id, nil
		}
		w.Info("pattern matched but use cap exhausted; falling back to human approval",
			wool.Field("pattern_id", pattern.ID),
			wool.ErrField(incErr))
	} else if patErr != nil {
		w.Warn("pattern lookup failed; falling back to human approval", wool.ErrField(patErr))
	}

	var id string
	if err := scoped.Within(ctx, func(ctx context.Context) error {
		var e error
		id, e = s.delegationStore().Insert(ctx, in, hash, expiresAt)
		return e
	}); err != nil {
		return "", w.Wrapf(err, "insert delegation grant")
	}
	w.Info("delegation requested",
		wool.Field("grant_id", id),
		wool.Field("expires_at", expiresAt.Format(time.RFC3339)))
	s.emit(ctx, in.ActorPrincipalID, "agent",
		"delegation.requested", "delegation_grant", id, in.OrgID)
	return id, nil
}

// WaitForDelegation streams the grant's status changes until
// terminal. The returned channel closes either:
//   - on terminal status (approved/denied/expired/cancelled)
//   - on ctx cancellation (caller disconnect)
//   - on store-level error (channel closes; check Subscribe error
//     return)
//
// Behavior on already-decided grants (caller subscribed AFTER
// the decision): the implementation reads the current row first
// and emits the terminal event immediately, then closes. No
// missed-NOTIFY race — even if the trigger fired before LISTEN
// started, the snapshot read catches it.
func (s *Service) WaitForDelegation(ctx context.Context, id, orgID string) (<-chan DelegationDecisionEvent, error) {
	w := wool.Get(ctx).In("WaitForDelegation", wool.Field("grant_id", id))
	if id == "" {
		return nil, w.NewError("grant id required")
	}
	return s.delegationStore().Subscribe(ctx, id, orgID)
}

// DecideDelegation transitions a pending grant. Returns the
// updated grant row.
//
// Authz: the caller must be an authorized grantor for this
// request. Validation happens at the RPC adapter (which has
// access to the calling principal's identity); business layer
// trusts the adapter's check.
//
// **Mint-on-approve flow.** This method ONLY transitions the
// row. The RPC handler is responsible for minting the scoped-
// auth token AFTER the atomic transition succeeds and calling
// SetMintedToken before the streaming event reaches the
// requestor. Two-step keeps the database transaction small and
// the mint code agnostic of DB internals.
func (s *Service) DecideDelegation(ctx context.Context, id, orgID, grantorID string, decision DelegationGrantStatus, reason string) (*DelegationGrant, error) {
	w := wool.Get(ctx).In("DecideDelegation",
		wool.Field("grant_id", id),
		wool.Field("decision", string(decision)))
	if id == "" {
		return nil, w.NewError("grant id required")
	}
	if grantorID == "" {
		return nil, w.NewError("grantor_id required")
	}
	if decision != GrantStatusApproved && decision != GrantStatusDenied {
		return nil, w.NewError("decision must be approved or denied (got %q)", decision)
	}
	if decision == GrantStatusDenied && strings.TrimSpace(reason) == "" {
		// Denials need a reason for the audit trail. The
		// approver UI MUST surface the prompt; reject here as
		// defense.
		return nil, w.NewError("denied decisions require a reason")
	}
	var grant *DelegationGrant
	if err := s.store.As(Identity{OrgID: orgID}).Within(ctx, func(ctx context.Context) error {
		var e error
		grant, e = s.delegationStore().Decide(ctx, id, orgID, grantorID, decision, reason)
		return e
	}); err != nil {
		return nil, w.Wrapf(err, "decide delegation")
	}
	w.Info("delegation decided",
		wool.Field("status", string(grant.Status)),
		wool.Field("grantor_id", grantorID))

	// Audit + notification fan-out. Audit is mandatory (the
	// approval-trail is a compliance requirement); notification
	// is best-effort (the inbox is a UX nicety).
	auditAction := "delegation.approved"
	if decision == GrantStatusDenied {
		auditAction = "delegation.denied"
	}
	s.emit(ctx, grantorID, "user", auditAction, "delegation_grant", grant.ID, grant.OrgID)
	s.notifyDelegationDecision(ctx, grant.ActorPrincipalID, grant.OrgID, grant.ID,
		string(decision), reason, grant.Action)
	return grant, nil
}

// SetMintedToken records the scoped-auth token's id after the
// DecideDelegation handler has minted it. Called from the RPC
// handler AFTER DecideDelegation returns approved.
func (s *Service) SetMintedToken(ctx context.Context, id, orgID, tokenID string) error {
	w := wool.Get(ctx).In("SetMintedToken",
		wool.Field("grant_id", id),
		wool.Field("token_id", tokenID))
	if tokenID == "" {
		return w.NewError("token_id required")
	}
	return s.store.As(Identity{OrgID: orgID}).Within(ctx, func(ctx context.Context) error {
		return s.delegationStore().SetMintedToken(ctx, id, orgID, tokenID)
	})
}

// GetDelegation fetches a single grant. Caller-supplied org_id
// scopes the query.
func (s *Service) GetDelegation(ctx context.Context, id, orgID string) (*DelegationGrant, error) {
	if id == "" {
		return nil, errors.New("grant id required")
	}
	var grant *DelegationGrant
	err := s.store.As(Identity{OrgID: orgID}).Within(ctx, func(ctx context.Context) error {
		var e error
		grant, e = s.delegationStore().Get(ctx, id, orgID)
		return e
	})
	return grant, err
}

// ListPendingDelegations returns paginated pending grants for
// the approver UI. orgID is required.
func (s *Service) ListPendingDelegations(ctx context.Context, orgID string, pageSize int32, pageToken string) ([]*DelegationGrant, string, error) {
	w := wool.Get(ctx).In("ListPendingDelegations", wool.Field("org_id", orgID))
	if orgID == "" {
		return nil, "", w.NewError("org_id required")
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	var grants []*DelegationGrant
	var next string
	err := s.store.As(Identity{OrgID: orgID}).Within(ctx, func(ctx context.Context) error {
		var e error
		grants, next, e = s.delegationStore().ListPending(ctx, orgID, pageSize, pageToken)
		return e
	})
	return grants, next, err
}

// =====================================================================
// Helpers
// =====================================================================

// delegationStore returns the typed accessor. Same pattern as
// principalStore() — keeps business code from reaching into
// untyped helpers.
func (s *Service) delegationStore() DelegationStore {
	if ds, ok := s.store.(DelegationStore); ok {
		return ds
	}
	panic("Service.store does not implement DelegationStore; see postgres_delegation_grants.go")
}

// canonicalContextKeys returns the request-context keys in
// deterministic order. Used when computing hashes / serializing
// the context for storage. Sorted slice keeps two
// json.Marshal-with-different-iteration-orders from producing
// different hashes.
func canonicalContextKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// notifyDelegationDecision posts an in-app inbox item to the
// principal's owning user when the principal is human (agents
// have no inbox; their requestors observe the streaming RPC
// directly). Best-effort: errors are logged and swallowed —
// failing the grant flow on inbox-write trouble would be a
// fragility tax we don't want.
//
// **Why "owning user" rather than "principal".** The
// notifications table keys by user_id (RLS-protected). Agent
// principals don't map to a user; surfacing their decision via
// the inbox would require a separate per-agent activity feed —
// out of scope here. M9 audit views give operators the
// cross-cutting visibility for now.
func (s *Service) notifyDelegationDecision(ctx context.Context, actorPrincipalID, orgID, grantID, status, reason, action string) {
	w := wool.Get(ctx).In("notifyDelegationDecision",
		wool.Field("grant_id", grantID),
		wool.Field("actor_principal_id", actorPrincipalID))

	p, err := s.GetPrincipal(ctx, actorPrincipalID)
	if err != nil {
		w.Info("cannot load actor for notification; skipping inbox post",
			wool.ErrField(err))
		return
	}
	if p.Kind != PrincipalKindHuman {
		// Agents/services don't have an inbox. The actor's parent
		// agent process consumes the WaitForDelegation stream
		// directly; that's enough surface.
		return
	}

	title := fmt.Sprintf("Delegation %s: %s", status, action)
	body := reason
	if body == "" {
		body = fmt.Sprintf("Your %s request was %s.", action, status)
	}
	notifType := "info"
	if status == "denied" {
		notifType = "warning"
	} else if status == "approved" {
		notifType = "success"
	}

	if _, err := s.CreateNotification(ctx, CreateNotificationInput{
		UserID: p.ID, OrgID: orgID, Title: title, Body: body,
		Type: notifType, Category: NotificationCategoryProduct,
	}); err != nil {
		// Inbox failure is not critical — the streaming RPC
		// already delivered the verdict. Log loud so operators
		// can correlate with notification-table outages.
		w.Warn("delegation inbox post failed",
			wool.ErrField(err))
	}
}
