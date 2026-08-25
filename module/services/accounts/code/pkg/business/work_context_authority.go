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
