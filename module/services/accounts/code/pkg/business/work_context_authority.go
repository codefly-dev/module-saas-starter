package business

import "context"

// WorkContextPermission is one exact RBAC decision that must be true before
// Accounts may sign it into a Work Context. Empty ResourceID means only an
// unscoped role assignment may grant it; a resource-scoped assignment must
// never widen into wildcard authority.
type WorkContextPermission struct {
	ResourceKind string
	Action       string
	ResourceID   string
}

// WorkContextAuthorityFacts is the current, database-derived authority and
// attribution snapshot used by the Work Context issuer.
type WorkContextAuthorityFacts struct {
	OrganizationRevision uint64
	PrincipalRevision    uint64
	AttributionTeamIDs   []string
	Actor                *Principal
}

func (f WorkContextAuthorityFacts) EffectiveRevision() uint64 {
	if f.PrincipalRevision > f.OrganizationRevision {
		return f.PrincipalRevision
	}
	return f.OrganizationRevision
}

// WorkContextAuthorityStore is deliberately narrower than Store. The
// production Postgres implementation resolves facts and every requested
// permission through one service-postgres read-only, tenant/principal-bound
// transaction. Tests can exercise the issuer without implementing Accounts'
// entire persistence surface.
type WorkContextAuthorityStore interface {
	ResolveWorkContextAuthority(
		ctx context.Context,
		orgID string,
		ownerPrincipalID string,
		actorPrincipalID string,
		permissions []WorkContextPermission,
	) (*WorkContextAuthorityFacts, error)
}
