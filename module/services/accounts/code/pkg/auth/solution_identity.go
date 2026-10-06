package auth

import (
	"context"
	"fmt"
	"regexp"
)

type verifiedSolutionKey struct{}

// solutionIDPattern is the catalog identity a solution registers under: one
// lowercase segment usable as a path element and a routing key. It mirrors the
// rule the gateway's registration surface and the registry both validate, so a
// value that reached here through a credential this service did not verify
// itself is still held to the same shape.
var solutionIDPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]*[a-z0-9])?$`)

// maxSolutionIDLength matches the registry's own bound on a solution id;
// maxSolutionPublisherLength its bound on a publisher.
const (
	maxSolutionIDLength        = 128
	maxSolutionPublisherLength = 256
)

// VerifiedSolutionIdentity is what the gateway proved about a solution-scoped
// Work Context mint: which solution asked, and the publisher its credential
// names. Both are needed — the id selects the registration, and the publisher
// is checked against the one that owns it, so a secret re-provisioned to a
// different publisher cannot mint the boundary of a registration it does not
// own.
type VerifiedSolutionIdentity struct {
	SolutionID string
	Publisher  string
}

// CanonicalSolutionID validates a solution identity.
func CanonicalSolutionID(candidate string) (string, error) {
	if len(candidate) > maxSolutionIDLength || !solutionIDPattern.MatchString(candidate) {
		return "", fmt.Errorf("solution identity must be one lowercase catalog segment")
	}
	return candidate, nil
}

// WithVerifiedSolution records which registered solution a request is minting
// for, and the publisher that proved it (issue #1015).
//
// It is recorded only by an adapter that has already authenticated the gateway
// credential, exactly like WithVerifiedPublicOrigin: the gateway is what proved
// the solution's own signed, solution-bound registration credential, and the
// headers it stamps are stripped from caller input on every transport. Nothing
// derives either value from a request field — that is the whole point of the
// boundary they select.
//
// A publisher is required. The gateway stamps it from the same claims as the
// id, so an assertion carrying one and not the other lost half of itself on the
// way, and admitting it would skip the ownership check entirely.
func WithVerifiedSolution(ctx context.Context, id, publisher string) (context.Context, error) {
	solution, err := CanonicalSolutionID(id)
	if err != nil {
		return ctx, err
	}
	if publisher == "" || len(publisher) > maxSolutionPublisherLength {
		return ctx, fmt.Errorf("solution identity must name its publisher")
	}
	return context.WithValue(ctx, verifiedSolutionKey{}, VerifiedSolutionIdentity{
		SolutionID: solution,
		Publisher:  publisher,
	}), nil
}

// VerifiedSolution reports the registered solution this request mints for, and
// whether there is one. Absent means an ordinary mint, which is every mint the
// host's own pages and every composed module make.
func VerifiedSolution(ctx context.Context) (VerifiedSolutionIdentity, bool) {
	identity, ok := ctx.Value(verifiedSolutionKey{}).(VerifiedSolutionIdentity)
	return identity, ok && identity.SolutionID != ""
}
