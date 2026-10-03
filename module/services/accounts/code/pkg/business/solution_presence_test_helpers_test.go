//go:build !pure

package business_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// Declared presence, for the business tests that install something.
//
// An installation names a solution TARGET, and InstallSolution resolves that
// target through the catalogue — accepted applied presence — before it writes
// anything. So a business test that installs has to declare presence first.
//
// This is the business package's own copy of the infra helper rather than a
// shared one: the two packages have separate test databases and separate
// harnesses (`testStore` is the same type but a different instance), and a
// shared helper would have to be exported from production code to be reachable
// from both. A test-only duplicate is the cheaper of the two, and it is
// deliberately written through the same store methods the reconcile pass uses,
// so neither copy can construct a presence state the reconciler could not.

// uniqueAlias makes a route alias no other test in this package has declared.
// `solution_targets_live_solution` is unique over the alias of every OPEN target
// and the package shares one database, so a fixed alias would collide between
// tests and fail inside the helper.
func uniqueAlias(label string) string {
	return label + "-" + strings.ToLower(strings.ReplaceAll(business.NewIDString(), "-", ""))[:12]
}

// fixtureDigest is a well-formed digest for a fixture: the schema requires
// `sha256:` plus exactly 64 hex characters, so a readable stand-in like
// "sha256:<binding id>" is refused by a CHECK constraint rather than quietly
// stored — which is the constraint doing its job and a helper not doing its own.
func fixtureDigest(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// declarePresence opens a live target for a fresh alias and records the applied
// generation that makes it ACCEPTED, returning (targetID, alias).
func declarePresence(t *testing.T, label string) (string, string) {
	t.Helper()
	alias := uniqueAlias(label)
	bindingID := "acme.test." + alias
	now := time.Now().UTC()
	entry := business.SolutionHostBindingGeneration{
		Generation: 1,
		Digest:     fixtureDigest(bindingID),
		Document:   "{}",
		At:         now,
	}
	var targetID string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		target, err := testStore.OpenSolutionTarget(ctx, bindingID, alias, 1, now)
		if err != nil {
			return err
		}
		targetID = target.ID
		return testStore.SaveSolutionHostBinding(ctx, &business.SolutionHostBindingRecord{
			BindingID:      bindingID,
			HostCoordinate: "acme/local/host",
			HostComponent:  "saas-starter",
			Desired:        &entry,
			Applied: &business.SolutionHostBindingApplied{
				SolutionHostBindingGeneration: entry,
				SolutionID:                    alias,
				Routes:                        []string{alias},
				// Non-empty deliberately: core refuses an applied record with no
				// ownership domain, and '' passes a whole-or-absent constraint
				// while being exactly the value it rejects.
				Domain:  "alpha",
				Release: "acme/" + alias + "@1.0.0",
			},
			UpdatedAt: now,
		})
	}))
	return targetID, alias
}
