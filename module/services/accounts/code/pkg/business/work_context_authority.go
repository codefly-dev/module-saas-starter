package business

import (
	"context"
	"errors"
)

var (
	ErrWorkContextAuthorizationStale = errors.New("work context authorization revision is stale")
	ErrEvidenceReadDenied            = errors.New("evidence read is not authorized")
)

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
	// ResolveInstallationAuthority is the headless (no-user-present) counterpart.
	// It resolves the installation's owner of record (fail-closed if none of the
	// owner/co-owners is currently an org admin), the agent principal actor
	// (fail-closed if revoked or disabled), and every requested permission against
	// the agent's STANDING scope grants (not owner ∩ actor RBAC). Fails with
	// ErrTypePermission when a requested scope is outside the standing grant.
	ResolveInstallationAuthority(
		ctx context.Context,
		orgID string,
		installationID string,
		permissions []WorkContextPermission,
	) (*InstallationAuthorityFacts, error)
}

// InstallationAuthorityFacts is the headless-mint authority snapshot: the owner
// of record chosen live, the agent actor, and the sealed revision. Unlike the
// delegated path the authority is the agent's own standing grants, so there is
// no separate owner authority slice to intersect with.
type InstallationAuthorityFacts struct {
	OwnerPrincipalID       string
	Actor                  *Principal
	OrganizationRevision   uint64
	OwnerPrincipalRevision uint64
	AttributionTeamIDs     []string
}

func (f InstallationAuthorityFacts) EffectiveRevision() uint64 {
	if f.OwnerPrincipalRevision > f.OrganizationRevision {
		return f.OwnerPrincipalRevision
	}
	return f.OrganizationRevision
}

// WorkContextRevisionSubject is one current principal authority slice a
// consumer must revalidate before accepting a signed Work Context effect.
type WorkContextRevisionSubject struct {
	PrincipalID string
	Permissions []WorkContextPermission
}

// WorkContextConsumerAuthorityStore is the internal consumer-side authority
// seam. It remains separate from issuance so an Accounts deployment can issue
// Work Contexts without accidentally claiming support for current consumer
// checks, and consumers cannot obtain a generic permission oracle. Empty
// Evidence owner/Task filters are meaningful: they describe a broader read
// that the implementation must never authorize from an exact-resource grant.
type WorkContextConsumerAuthorityStore interface {
	CheckWorkContextAuthorizationRevision(
		ctx context.Context,
		orgID string,
		ownerPrincipalID string,
		expectedRevision uint64,
		subjects []WorkContextRevisionSubject,
	) error
	AuthorizeEvidenceRead(
		ctx context.Context,
		orgID string,
		callerPrincipalID string,
		ownerPrincipalID string,
		taskID string,
		sessionID string,
	) error
}
