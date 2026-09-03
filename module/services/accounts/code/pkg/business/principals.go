// Package business — principals: the unified identity model.
//
// **Why this file exists alongside users.go and api_keys.go.**
// Humans live in `users`, services live in `api_keys`, but the auth
// layer (CheckPermission, role assignments, audit) wants to treat
// every authenticatable thing as a single concept: a Principal.
// Migration 36 adds the `principals` table; this file is the
// business-layer surface that codefly host calls into for identity
// resolution and agent registration.
//
// **Why a hand-rolled struct rather than gen.Principal.** The proto
// changes for the Decide RPC + Principal messages land together in
// M2 — keeping them coherent in one proto regen avoids partial
// schemas. M1 ships the schema + the Go API; M2 replaces this
// struct's wire shape with the proto-generated equivalent.
//
// The Principal-side RPCs (Get / List / CreateAgent / Revoke) DO NOT
// exist in proto yet — they're internal Go API for now. M2 promotes
// them to RPCs when the proto changes land.
package business

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"
)

// PrincipalKind values mirror the SQL CHECK constraint and the
// codefly-side policy.Principal kinds. Drift here breaks the wire
// format; the constants exist to make the drift detectable at
// compile time.
const (
	PrincipalKindHuman   = "human"
	PrincipalKindService = "service"
	PrincipalKindAgent   = "agent"
)

// Principal is the resolved identity row. Mirrors the principals
// table 1:1.
type Principal struct {
	ID              string
	Kind            string
	DisplayName     string
	OrgID           string // empty for kind=human (cross-org)
	AgentIdentifier string // "publisher/name:version" for kind=agent
	// The principal that created this one — empty = root (humans).
	CreatedBy     string
	CreatedAt     time.Time
	RevokedAt     *time.Time
	RevokedReason string
	// Delegated-execution ceiling, kind=agent only. Nil = unrestricted.
	AllowedAudiences []string
	AllowedScopes    []string
	// A reversible suspension of a delegated actor, distinct from RevokedAt.
	DisabledAt     *time.Time
	DisabledReason string
}

// IsRevoked reports whether the principal has been revoked. Used by
// the PDP to short-circuit decisions for revoked principals before
// hitting the role-assignment join.
func (p *Principal) IsRevoked() bool {
	return p != nil && p.RevokedAt != nil
}

// IsDisabled reports whether the principal is under a reversible suspension.
// A disabled agent is not an eligible Work Context actor, but unlike a revoked
// one it can be re-enabled.
func (p *Principal) IsDisabled() bool {
	return p != nil && p.DisabledAt != nil
}

// Validate checks the in-memory invariants that the SQL constraints
// also enforce. Run before INSERT for a friendly error vs the SQL
// CHECK violation. Mirrors policy/principal.go's Validate but for
// the saas-starter side of the wire.
func (p *Principal) Validate() error {
	if p == nil {
		return errors.New("principal: nil")
	}
	if p.ID == "" {
		return errors.New("principal: empty ID")
	}
	if p.DisplayName == "" {
		return errors.New("principal: empty DisplayName")
	}
	switch p.Kind {
	case PrincipalKindHuman:
		if p.OrgID != "" {
			return errors.New("principal: human kind must have empty OrgID (humans are cross-org)")
		}
		if p.AgentIdentifier != "" {
			return errors.New("principal: human kind must have empty AgentIdentifier")
		}
	case PrincipalKindService:
		if p.OrgID == "" {
			return errors.New("principal: service kind requires OrgID")
		}
		if p.AgentIdentifier != "" {
			return errors.New("principal: service kind must have empty AgentIdentifier")
		}
	case PrincipalKindAgent:
		if p.OrgID == "" {
			return errors.New("principal: agent kind requires OrgID")
		}
		if p.AgentIdentifier == "" {
			return errors.New("principal: agent kind requires AgentIdentifier")
		}
		if !looksLikeAgentIdentifier(p.AgentIdentifier) {
			return fmt.Errorf("principal: AgentIdentifier %q must be 'publisher/name:version'", p.AgentIdentifier)
		}
	case "":
		return errors.New("principal: empty Kind")
	default:
		return fmt.Errorf("principal: unknown Kind %q (want human|service|agent)", p.Kind)
	}
	if p.Kind != PrincipalKindAgent && (len(p.AllowedAudiences) > 0 || len(p.AllowedScopes) > 0) {
		return errors.New("principal: allowed audiences/scopes are an agent-only ceiling")
	}
	return nil
}

// looksLikeAgentIdentifier is a structural check (not a strict spec
// validator). The full canonical shape is "publisher/name:version".
// We refuse strings without the slash + colon at install time so a
// typo in agent.codefly.yaml doesn't sneak past as a "valid" agent.
func looksLikeAgentIdentifier(s string) bool {
	slash := strings.IndexByte(s, '/')
	colon := strings.IndexByte(s, ':')
	if slash <= 0 || colon <= slash+1 || colon == len(s)-1 {
		return false
	}
	return true
}

// CreateAgentRequest is the inbound shape for registering a new
// agent principal. Used by the CLI when a user installs a plugin
// into their org. The principals.id is generated server-side.
type CreateAgentRequest struct {
	OrgID           string
	AgentIdentifier string // "publisher/name:version"
	DisplayName     string
	// The authenticated principal performing the create (the authorship root).
	CreatedBy string
	// Optional delegated-execution ceiling. Empty = unrestricted.
	AllowedAudiences []string
	AllowedScopes    []string
}

// PrincipalStore is the persistence surface this file calls into.
// Defined here (not in infra) so business depends on a narrow
// interface, not on the full PostgresStore shape — making unit-style
// tests of the business layer easier when we add them.
type PrincipalStore interface {
	GetPrincipal(ctx context.Context, id string) (*Principal, error)
	GetAgentPrincipal(ctx context.Context, orgID, agentIdentifier string) (*Principal, error)
	CreateAgentPrincipal(ctx context.Context, p *Principal) error
	RevokePrincipal(ctx context.Context, id, reason string) error
	DisableAgentPrincipal(ctx context.Context, id, reason string) error
	EnableAgentPrincipal(ctx context.Context, id string) error
	ListPrincipals(ctx context.Context, orgID, kind string, pageSize int32, pageToken string) ([]*Principal, string, error)
}

// principalStore returns the store half of the Service that
// implements PrincipalStore. The store is the same PostgresStore
// the rest of the service uses; this method is a typed accessor so
// the principals business code doesn't reach into untyped helpers.
//
// Keeping this here (rather than in users.go pattern) signals that
// principals operations are intentionally a narrow surface — not
// every CRUD on principals belongs in the business layer; some
// (like the backfill) live entirely in SQL migrations.
func (s *Service) principalStore() PrincipalStore {
	// PostgresStore implements PrincipalStore via postgres_principals.go.
	// Compile-time interface satisfaction is asserted at the bottom of
	// that file with `var _ PrincipalStore = (*PostgresStore)(nil)`.
	if ps, ok := s.store.(PrincipalStore); ok {
		return ps
	}
	// The store currently in production is always PostgresStore. A
	// nil return here would crash unhelpfully; the panic surfaces
	// the wiring bug at the call site instead.
	panic("Service.store does not implement PrincipalStore; see postgres_principals.go")
}

// actorTypeForCreator resolves the audit actor_type facet from the kind of the
// principal that performed an action. Falls back to "user" when the creator
// can't be resolved (empty, bootstrap, or already-revoked id) — the dominant
// CLI-install case is a human. Without this, agent- or service-initiated creates
// would be mislabeled "user" in the audit trail.
func (s *Service) actorTypeForCreator(ctx context.Context, principalID string) string {
	if principalID == "" {
		return "user"
	}
	creator, err := s.GetPrincipal(ctx, principalID)
	if err != nil {
		return "user"
	}
	switch creator.Kind {
	case PrincipalKindAgent:
		return "agent"
	case PrincipalKindService:
		return "service"
	default:
		return "user"
	}
}

// GetPrincipal returns a principal by ID. Returns ErrTypeNotFound
// (wrapped) when the ID doesn't exist or has been revoked.
//
// Why revoked principals are NOT returned: callers of this method
// are typically authorizing a fresh action; a revoked principal
// should fail closed at the lookup. Audit-log reads that need the
// historical principal use a different code path (and a different
// SQL query without the revoked filter) — to be added in M9.
func (s *Service) GetPrincipal(ctx context.Context, id string) (*Principal, error) {
	w := wool.Get(ctx).In("GetPrincipal", wool.Field("principal_id", id))
	if id == "" {
		return nil, w.NewError("principal id required")
	}
	// Point lookup by id (no tenant to scope to); RPC-layer authz gates this.
	// principals RLS protects enumeration (ListPrincipals), not id lookups.
	var p *Principal
	if err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		var e error
		p, e = s.principalStore().GetPrincipal(ctx, id)
		return e
	}); err != nil {
		return nil, w.Wrapf(err, "cannot get principal")
	}
	if p.IsRevoked() {
		return nil, NewStoreError(
			fmt.Errorf("principal %s revoked at %s: %s", id, p.RevokedAt.Format(time.RFC3339), p.RevokedReason),
			ErrTypeNotFound,
		)
	}
	// A disabled principal is intentionally still returned here: admin lifecycle
	// operations (revoke, re-enable) resolve their target through this method, so
	// hiding disabled rows would make a disabled agent unrevocable. The Work
	// Context mint path fails a disabled actor closed in ResolveWorkContextAuthority,
	// and the delegation mint fails it closed at its own call sites.
	return p, nil
}

// GetAgentPrincipal looks up an agent by its canonical identifier
// within an org. Returns ErrTypeNotFound if the agent isn't
// installed or has been revoked. Used by the CLI's `manager.Load`
// path to resolve "this agent's principal" before minting tokens.
func (s *Service) GetAgentPrincipal(ctx context.Context, orgID, agentIdentifier string) (*Principal, error) {
	w := wool.Get(ctx).In("GetAgentPrincipal",
		wool.Field("org_id", orgID),
		wool.Field("agent_id", agentIdentifier))
	if orgID == "" {
		return nil, w.NewError("org_id required")
	}
	if agentIdentifier == "" {
		return nil, w.NewError("agent_identifier required")
	}
	if !looksLikeAgentIdentifier(agentIdentifier) {
		return nil, w.NewError("agent_identifier must be 'publisher/name:version'")
	}
	var p *Principal
	if err := s.store.As(Identity{OrgID: orgID}).Within(ctx, func(ctx context.Context) error {
		var e error
		p, e = s.principalStore().GetAgentPrincipal(ctx, orgID, agentIdentifier)
		return e
	}); err != nil {
		return nil, w.Wrapf(err, "cannot get agent principal")
	}
	if p.IsRevoked() {
		return nil, NewStoreError(
			fmt.Errorf("agent %s in org %s revoked", agentIdentifier, orgID),
			ErrTypeNotFound,
		)
	}
	if p.IsDisabled() {
		return nil, NewStoreError(
			fmt.Errorf("agent %s in org %s disabled", agentIdentifier, orgID),
			ErrTypeNotFound,
		)
	}
	return p, nil
}

// CreateAgentPrincipal registers a new agent in the org. Idempotent
// on (org_id, agent_identifier) — calling twice with the same
// identifier returns the existing principal rather than failing.
// This matches `codefly install` semantics: re-installing the same
// version is a no-op.
//
// **NOT idempotent across versions.** Installing
// publisher/agent:0.1.0 and then publisher/agent:0.2.0 produces TWO
// principals — the version is part of the identifier on purpose.
// Permissions assigned to v0.1.0 do NOT automatically apply to
// v0.2.0; the user's review of permissions is per-version, by design.
func (s *Service) CreateAgentPrincipal(ctx context.Context, req CreateAgentRequest) (*Principal, error) {
	w := wool.Get(ctx).In("CreateAgentPrincipal",
		wool.Field("org_id", req.OrgID),
		wool.Field("agent_id", req.AgentIdentifier))

	if req.OrgID == "" {
		return nil, w.NewError("org_id required")
	}
	if req.AgentIdentifier == "" {
		return nil, w.NewError("agent_identifier required")
	}
	if req.DisplayName == "" {
		// Default to the agent identifier — matches the behavior the
		// CLI surfaces in its "installing X" prompt.
		req.DisplayName = req.AgentIdentifier
	}

	// Idempotency: if the agent already exists in the org, return it.
	var existing *Principal
	err := s.store.As(Identity{OrgID: req.OrgID}).Within(ctx, func(ctx context.Context) error {
		var e error
		existing, e = s.principalStore().GetAgentPrincipal(ctx, req.OrgID, req.AgentIdentifier)
		return e
	})
	if err == nil && !existing.IsRevoked() {
		w.Trace("agent principal already registered; returning existing",
			wool.Field("principal_id", existing.ID))
		return existing, nil
	}
	if err != nil {
		var se *StoreError
		if !errors.As(err, &se) || se.StoreErrorType != ErrTypeNotFound {
			return nil, w.Wrapf(err, "cannot check for existing agent principal")
		}
	}

	// Generate ID server-side. We don't reuse user/api_key IDs for
	// agents — agents are net-new principals.
	p := &Principal{
		ID:               NewIDString(),
		Kind:             PrincipalKindAgent,
		DisplayName:      req.DisplayName,
		OrgID:            req.OrgID,
		AgentIdentifier:  req.AgentIdentifier,
		CreatedBy:        req.CreatedBy,
		CreatedAt:        time.Now().UTC(),
		AllowedAudiences: req.AllowedAudiences,
		AllowedScopes:    req.AllowedScopes,
	}
	if err := p.Validate(); err != nil {
		return nil, w.Wrapf(err, "invalid principal")
	}
	if err := s.store.As(Identity{OrgID: req.OrgID}).Within(ctx, func(ctx context.Context) error {
		return s.principalStore().CreateAgentPrincipal(ctx, p)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot create agent principal")
	}
	s.emit(ctx, req.CreatedBy, s.actorTypeForCreator(ctx, req.CreatedBy), EventPrincipalCreated, "principal", p.ID, p.OrgID,
		map[string]any{"agent_identifier": p.AgentIdentifier})
	w.Info("agent principal created",
		wool.Field("principal_id", p.ID),
		wool.Field("agent_id", p.AgentIdentifier))
	return p, nil
}

// RevokePrincipal marks a principal as revoked. Idempotent: revoking
// an already-revoked principal is a no-op (the original revoked_at
// and reason are preserved). Returns ErrTypeNotFound if the principal
// doesn't exist.
//
// Revoking a HUMAN principal cascades: the matching `users` row's
// status is also set to 'inactive' for consistency. We don't
// reverse-cascade (revoking a user doesn't currently revoke their
// principal) — that's a deliberate gap, kept for backwards compat
// with code that reads users directly. A future migration unifies
// the lifecycle.
func (s *Service) RevokePrincipal(ctx context.Context, id, reason string) error {
	w := wool.Get(ctx).In("RevokePrincipal",
		wool.Field("principal_id", id))
	if id == "" {
		return w.NewError("principal id required")
	}
	if reason == "" {
		// Don't allow silent revocations. Audit value depends on a
		// readable reason being recorded.
		return w.NewError("reason required (no silent revocations)")
	}
	// Privileged admin op by id (cascades to users for humans); RPC authz gates
	// it, and the cascade needs cross-table reach → System. Load the principal in
	// the same System tx to recover its org (this method takes only id+reason) and
	// to detect whether the call actually changed state, so idempotent repeats
	// don't emit a duplicate audit event.
	var orgID string
	var alreadyRevoked bool
	if err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		p, e := s.principalStore().GetPrincipal(ctx, id)
		if e != nil {
			return e
		}
		orgID = p.OrgID
		alreadyRevoked = p.IsRevoked()
		return s.principalStore().RevokePrincipal(ctx, id, reason)
	}); err != nil {
		return w.Wrapf(err, "cannot revoke principal")
	}
	if !alreadyRevoked {
		s.emit(ctx, "system", "system", EventPrincipalRevoked, "principal", id, orgID,
			map[string]any{"reason": reason})
	}
	w.Info("principal revoked", wool.Field("reason", reason))
	return nil
}

// DisableAgentPrincipal reversibly suspends an agent principal. Idempotent:
// re-disabling preserves the original disabled_at/reason and emits no duplicate
// audit event. The suspension bumps the org/principal authorization revision, so
// outstanding Work Contexts naming this actor go stale immediately and no new
// one mints until it is re-enabled.
func (s *Service) DisableAgentPrincipal(ctx context.Context, id, reason string) error {
	w := wool.Get(ctx).In("DisableAgentPrincipal", wool.Field("principal_id", id))
	if id == "" {
		return w.NewError("principal id required")
	}
	if reason == "" {
		return w.NewError("reason required (no silent disables)")
	}
	var orgID string
	var alreadyDisabled bool
	if err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		p, e := s.principalStore().GetPrincipal(ctx, id)
		if e != nil {
			return e
		}
		if p.Kind != PrincipalKindAgent {
			return NewStoreError(errors.New("only agent principals can be disabled"), ErrTypeConflict)
		}
		if p.IsRevoked() {
			return NewStoreError(errors.New("a revoked principal cannot be disabled"), ErrTypeConflict)
		}
		orgID = p.OrgID
		alreadyDisabled = p.IsDisabled()
		return s.principalStore().DisableAgentPrincipal(ctx, id, reason)
	}); err != nil {
		return w.Wrapf(err, "cannot disable agent principal")
	}
	if !alreadyDisabled {
		s.emit(ctx, "system", "system", EventPrincipalDisabled, "principal", id, orgID,
			map[string]any{"reason": reason})
	}
	w.Info("agent principal disabled", wool.Field("reason", reason))
	return nil
}

// EnableAgentPrincipal lifts a reversible suspension. Idempotent: enabling an
// already-active agent is a no-op with no audit event. A revoked principal is
// terminal and cannot be re-enabled.
func (s *Service) EnableAgentPrincipal(ctx context.Context, id string) error {
	w := wool.Get(ctx).In("EnableAgentPrincipal", wool.Field("principal_id", id))
	if id == "" {
		return w.NewError("principal id required")
	}
	var orgID string
	var wasDisabled bool
	if err := s.store.As(System()).Within(ctx, func(ctx context.Context) error {
		p, e := s.principalStore().GetPrincipal(ctx, id)
		if e != nil {
			return e
		}
		if p.Kind != PrincipalKindAgent {
			return NewStoreError(errors.New("only agent principals can be enabled"), ErrTypeConflict)
		}
		if p.IsRevoked() {
			return NewStoreError(errors.New("a revoked principal cannot be re-enabled"), ErrTypeConflict)
		}
		orgID = p.OrgID
		wasDisabled = p.IsDisabled()
		return s.principalStore().EnableAgentPrincipal(ctx, id)
	}); err != nil {
		return w.Wrapf(err, "cannot enable agent principal")
	}
	if wasDisabled {
		s.emit(ctx, "system", "system", EventPrincipalEnabled, "principal", id, orgID, nil)
	}
	w.Info("agent principal enabled")
	return nil
}

// ListPrincipals returns paginated principals in an org, optionally
// filtered by kind. orgID is required (no cross-org listing in this
// API; admin tools that need cross-org use a different code path).
//
// kind is the empty string to list all kinds, or one of human /
// service / agent. Other values return an error rather than a
// silent empty list (helps debug typos).
func (s *Service) ListPrincipals(ctx context.Context, orgID, kind string, pageSize int32, pageToken string) ([]*Principal, string, error) {
	w := wool.Get(ctx).In("ListPrincipals",
		wool.Field("org_id", orgID),
		wool.Field("kind", kind))
	if orgID == "" {
		return nil, "", w.NewError("org_id required")
	}
	switch kind {
	case "", PrincipalKindHuman, PrincipalKindService, PrincipalKindAgent:
		// ok
	default:
		return nil, "", w.NewError("kind must be empty, 'human', 'service', or 'agent'")
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	var out []*Principal
	var next string
	if err := s.store.As(Identity{OrgID: orgID}).Within(ctx, func(ctx context.Context) error {
		var e error
		out, next, e = s.principalStore().ListPrincipals(ctx, orgID, kind, pageSize, pageToken)
		return e
	}); err != nil {
		return nil, "", err
	}
	return out, next, nil
}
