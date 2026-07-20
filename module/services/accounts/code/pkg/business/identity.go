package business

import "context"

// Identity is the principal a unit of data access acts as. A Store handle bound
// to an Identity (see infra.PostgresStore.As) derives the RLS session context —
// app.current_user_id and/or app.current_org_id — from it on every call, so the
// identity decision is made once (at the edge, from the authenticated caller)
// rather than each call site choosing WithUserTx / WithOrgTx / WithControlPlane by hand.
//
// System() is the single, explicit, audited way past RLS — greppable by design.
type Identity struct {
	UserID string // "" for non-user principals
	OrgID  string // tenant scope; "" = no tenant
	Kind   string // PrincipalKind{Human,Service,Agent}; informational
	system bool   // set only by System()
}

// System is the one explicit RLS bypass, for genuine cross-tenant / bootstrap
// work (the role that the scattered WithControlPlane call sites collapse into). Use
// sparingly; it is audited via infra.ControlPlaneCounters.
func System() Identity { return Identity{system: true} }

// IsSystem reports whether this identity bypasses RLS.
func (i Identity) IsSystem() bool { return i.system }

// Scoped is a Store handle bound to an Identity (see Store.As). Within runs fn
// in one transaction carrying the identity's RLS context — app.current_user_id
// and/or app.current_org_id, or the explicit bypass for System(). Existing Store
// methods called inside fn pick up the tx via the ctx, so identity is set once
// at the entry. Promoted domain methods hang off Scoped as slices migrate; this
// interface grows with them (it lives in business to avoid an infra import cycle;
// infra.PostgresStore provides the implementation).
type Scoped interface {
	Within(ctx context.Context, fn func(ctx context.Context) error) error
	Identity() Identity

	// principals / org slice (migrated)
	ListPrincipals(ctx context.Context, kind string, pageSize int32, pageToken string) ([]*Principal, string, error)
	AddOrgMember(ctx context.Context, userID, role string) error
	GetPrincipal(ctx context.Context, id string) (*Principal, error)
	GetAgentPrincipal(ctx context.Context, orgID, agentIdentifier string) (*Principal, error)
	CreateAgentPrincipal(ctx context.Context, p *Principal) error
	RevokePrincipal(ctx context.Context, id, reason string) error
}
