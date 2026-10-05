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

// maxSolutionIDLength matches the registry's own bound on a solution id.
const maxSolutionIDLength = 128

// CanonicalSolutionID validates a solution identity.
func CanonicalSolutionID(candidate string) (string, error) {
	if len(candidate) > maxSolutionIDLength || !solutionIDPattern.MatchString(candidate) {
		return "", fmt.Errorf("solution identity must be one lowercase catalog segment")
	}
	return candidate, nil
}

// WithVerifiedSolution records which registered solution a request is minting
// for (issue #1015).
//
// It is recorded only by an adapter that has already authenticated the gateway
// credential, exactly like WithVerifiedPublicOrigin: the gateway is what proved
// the solution's own signed, solution-bound registration credential, and the
// header it stamps is stripped from caller input on every transport. Nothing
// derives this from a request field — that is the whole point of the boundary
// it selects.
func WithVerifiedSolution(ctx context.Context, candidate string) (context.Context, error) {
	solution, err := CanonicalSolutionID(candidate)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, verifiedSolutionKey{}, solution), nil
}

// VerifiedSolution reports the registered solution this request mints for, and
// whether there is one. Absent means an ordinary mint, which is every mint the
// host's own pages and every composed module make.
func VerifiedSolution(ctx context.Context) (string, bool) {
	solution, ok := ctx.Value(verifiedSolutionKey{}).(string)
	return solution, ok && solution != ""
}
