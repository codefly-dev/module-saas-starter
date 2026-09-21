package auth

import (
	"context"
	"errors"
	"regexp"

	"github.com/google/uuid"
)

// ErrRequestIdentityMalformed means a trusted transport carried an identity
// field that does not parse. The projection refuses it rather than dropping the
// field, because a dropped acting-as value silently downgrades an impersonated
// request to the actor's own authority.
var ErrRequestIdentityMalformed = errors.New("verified request identity is malformed")

// RequestIdentity is the one typed identity every transport projects onto a
// request context. It keeps apart the two questions authorization and
// accountability answer differently:
//
//   - EffectiveSubject is the user whose authority the request runs under.
//     Tenant membership, scoped roles, resource authority and the user-scoped
//     database context all resolve for this id.
//   - RealActor is the person the request is attributable to. Audit records it,
//     and platform authority is withheld from the request entirely whenever the
//     two ids differ.
//
// For an ordinary session the two are the same id. They diverge only under
// impersonation, where a platform admin (RealActor) operates as a target user
// (EffectiveSubject).
//
// Delegation is a different relationship and stays a separate field: the RFC
// 8693 `act` chain names a service acting on behalf of the subject, never a
// user an admin is viewing as.
//
// ClientID answers a third question neither id answers: not who the request
// runs as or is attributable to, but what the person made it through. It is a
// registered client's id, empty for the host's own web session.
type RequestIdentity struct {
	RealActor        uuid.UUID
	EffectiveSubject uuid.UUID
	OrgID            uuid.UUID
	SessionID        uuid.UUID
	Delegation       *Actor
	ClientID         string
}

// Impersonated reports whether this request runs under someone else's subject.
func (r RequestIdentity) Impersonated() bool {
	return r.RealActor != uuid.Nil && r.EffectiveSubject != uuid.Nil && r.RealActor != r.EffectiveSubject
}

// RealActorID and EffectiveSubjectID render the two ids for the string-keyed
// store and audit APIs. A zero id renders empty rather than as the nil UUID, so
// a missing identity cannot be mistaken for a real principal.
func (r RequestIdentity) RealActorID() string        { return renderID(r.RealActor) }
func (r RequestIdentity) EffectiveSubjectID() string { return renderID(r.EffectiveSubject) }

func renderID(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

// RequestIdentityOf projects a locally verified access token onto the request
// contract. `sub` is the real actor throughout, including on an impersonation
// token; the `acting` claim, when present, names the effective subject, and
// `azp` the registered client the token was minted for.
func RequestIdentityOf(identity *Identity) RequestIdentity {
	projected := RequestIdentity{
		RealActor:        identity.UserID,
		EffectiveSubject: identity.UserID,
		OrgID:            identity.OrgID,
		SessionID:        identity.SessionID,
		Delegation:       identity.Actor,
		ClientID:         identity.ClientID,
	}
	if identity.ActingAsUserID != uuid.Nil {
		projected.EffectiveSubject = identity.ActingAsUserID
	}
	return projected
}

// OrdinaryRequestIdentity projects a principal acting for itself, where the real
// actor and the effective subject are the same user. Unlike the forwarded-field
// projection it cannot fail, because only an acting-as value can be malformed.
func OrdinaryRequestIdentity(userID, orgID string) RequestIdentity {
	subject := parseIDOrNil(userID)
	return RequestIdentity{
		RealActor:        subject,
		EffectiveSubject: subject,
		OrgID:            parseIDOrNil(orgID),
	}
}

// ParseRequestIdentity projects the identity fields a trusted gateway forwarded.
// An unparseable actor, org or session id degrades to the zero value the way
// WithVerifiedDatabaseIdentity does — the request simply carries no verified
// principal — but an unparseable acting-as id is an error: admitting it as "not
// impersonating" would run a support session with the actor's own authority.
func ParseRequestIdentity(userID, actingAsUserID, orgID, sessionID string) (RequestIdentity, error) {
	projected := OrdinaryRequestIdentity(userID, orgID)
	projected.SessionID = parseIDOrNil(sessionID)
	if actingAsUserID != "" {
		actingAs, err := uuid.Parse(actingAsUserID)
		if err != nil || actingAs == uuid.Nil {
			return RequestIdentity{}, ErrRequestIdentityMalformed
		}
		// An acting-as value is only meaningful next to the actor it is
		// attributed to. Accepting one over an unusable actor would leave the
		// target as the sole principal, which reads as an ordinary session and
		// so resolves the target's own platform authority.
		if projected.RealActor == uuid.Nil {
			return RequestIdentity{}, ErrRequestIdentityMalformed
		}
		projected.EffectiveSubject = actingAs
	}
	return projected, nil
}

// clientIDPattern is the shape of a registered client's id, the same one the
// registry admits and the `azp` claim carries.
var clientIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

// ParseClientID projects the registered client a trusted gateway named on the
// request. Empty is the ordinary case — the host's own web session names no
// client — and never an error.
//
// A value that is not a client id is refused rather than dropped the way a
// malformed delegation chain is. The chain is an annotation on a request that
// is authorized either way, whereas this value is written into an append-only
// compliance table and read back as evidence that a call was not first-party;
// admitting an arbitrary string there would make that evidence unreliable in
// the one direction nobody can correct afterwards.
func ParseClientID(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if !clientIDPattern.MatchString(raw) {
		return "", ErrRequestIdentityMalformed
	}
	return raw, nil
}

func parseIDOrNil(raw string) uuid.UUID {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil
	}
	return parsed
}

// verifiedRequestIdentityKey is private for the same reason the database-scope
// key is: only an interceptor that verified a token or a trusted gateway
// assertion may install a request identity. Caller-controlled headers and wool
// context values cannot.
type verifiedRequestIdentityKey struct{}

// WithVerifiedRequestIdentity binds the projected identity to ctx. An identity
// missing either id is not installed, so a malformed principal fails closed to
// "unauthenticated" rather than to a blank subject.
//
// Both ids are required because Impersonated() answers "are these two different
// users", and a half-formed identity carrying only a subject answers that with
// "no" — indistinguishable from an ordinary session, and therefore able to
// resolve that subject's platform authority. Refusing it here keeps the
// impersonation predicate meaningful for every caller.
func WithVerifiedRequestIdentity(ctx context.Context, identity RequestIdentity) context.Context {
	if ctx == nil || identity.EffectiveSubject == uuid.Nil || identity.RealActor == uuid.Nil {
		return ctx
	}
	return context.WithValue(ctx, verifiedRequestIdentityKey{}, identity)
}

// VerifiedRequestIdentity returns the identity installed by an authentication
// interceptor. Absent means the request never crossed one.
func VerifiedRequestIdentity(ctx context.Context) (RequestIdentity, bool) {
	if ctx == nil {
		return RequestIdentity{}, false
	}
	identity, ok := ctx.Value(verifiedRequestIdentityKey{}).(RequestIdentity)
	return identity, ok
}

// ImpersonatedRequest reports whether ctx carries an impersonated identity. It
// is the single predicate every platform-authority gate consults, so neither
// the actor's own platform grants nor the target's can be resolved while a
// support session is acting as someone else.
func ImpersonatedRequest(ctx context.Context) bool {
	identity, ok := VerifiedRequestIdentity(ctx)
	return ok && identity.Impersonated()
}
