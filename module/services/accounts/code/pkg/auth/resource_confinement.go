package auth

// THE CONFINEMENT A CREDENTIAL CARRIES, as this service reads it
// (register finding SA-F-MCPAUD, round 3).
//
// A credential issued for one consumer — an RFC 8707 resource indicator naming a
// solution's MCP endpoint — is held by a third-party client and, once forwarded,
// by the solution runtime it dialled. The perimeter admits such a credential at
// the Work Context mint, because the consumer's own work needs a capability
// minted on the person's behalf. What the mint may then ISSUE has to be bounded
// by the consumer, or a credential delivered to one solution mints the person's
// whole authority, for any audience, in any organization they belong to.
//
// The perimeter cannot bound it: the ceiling is an installation's, and the
// perimeter is not the authority on installations. So the perimeter states two
// facts and this service decides from them — which resource the credential names,
// and which installation admitted the request. Both arrive as headers the
// perimeter owns, and both are read ONLY behind a verified gateway credential, on
// the same footing and through the same strip as the identity headers beside
// them (forwardedIdentityHeaders): a caller that could set either would choose
// its own ceiling.
//
// Recorded as two separate facts rather than one, because they fail differently.
// A confinement with no installation is a perimeter that admitted a confined
// credential without resolving its consent, which must refuse; an installation
// with no confinement is an ordinary session's request, which is every other
// request this service serves.

import (
	"context"
	"strings"
)

type (
	verifiedResourceConfinementKey struct{}
	verifiedInstallationKey        struct{}
)

// WithVerifiedResourceConfinement records the resource a credential is confined
// to. The value is the indicator as issued, compared by equality and never
// re-rendered, so it is stored exactly as the perimeter stated it.
//
// An empty candidate records NOTHING, and that is the whole point of a presence
// test rather than a string comparison: the perimeter stamps the header on every
// allow decision and leaves it empty for an unbound session, so "" is "this
// credential is not confined" and must not be mistaken for a confinement to the
// empty resource.
func WithVerifiedResourceConfinement(ctx context.Context, candidate string) context.Context {
	if strings.TrimSpace(candidate) == "" {
		return ctx
	}
	return context.WithValue(ctx, verifiedResourceConfinementKey{}, candidate)
}

// VerifiedResourceConfinement reports the resource this request's credential is
// confined to, and whether it is confined at all.
func VerifiedResourceConfinement(ctx context.Context) (string, bool) {
	resource, ok := ctx.Value(verifiedResourceConfinementKey{}).(string)
	return resource, ok && resource != ""
}

// WithVerifiedInstallation records which installation the perimeter's authority
// admitted this request under.
func WithVerifiedInstallation(ctx context.Context, candidate string) context.Context {
	if strings.TrimSpace(candidate) == "" {
		return ctx
	}
	return context.WithValue(ctx, verifiedInstallationKey{}, candidate)
}

// VerifiedInstallation reports the installation the perimeter admitted this
// request under, and whether it named one.
func VerifiedInstallation(ctx context.Context) (string, bool) {
	installation, ok := ctx.Value(verifiedInstallationKey{}).(string)
	return installation, ok && installation != ""
}
