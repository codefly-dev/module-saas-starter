//go:build !pure

package infra_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// Declared presence, for the tests that install something.
//
// An installation names a solution TARGET, and a target exists only because the
// host applied a present generation for a binding. So a test that installs has
// to declare presence first — there is no longer a free-text identifier it can
// invent. These helpers are that declaration, written through the store methods
// the reconcile pass itself uses rather than raw SQL, so a test cannot construct
// a presence state the reconciler could not.

// uniqueAlias makes a route alias that no other test in this package has
// declared. It is necessary, not tidiness: `solution_targets_live_solution` is a
// unique index over the alias of every OPEN target, and the package shares one
// database — so two tests declaring "audit" would collide, and the second would
// fail inside a helper with a unique-violation that says nothing about what it
// was testing.
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

// declarePresence opens a live solution target for alias and records the applied
// generation that makes it ACCEPTED, returning the target id.
//
// Both halves are required. The target alone would be a row the catalogue
// refuses: `ListAvailableSolutionTargets` joins the binding and requires an
// applied, non-tombstone generation, because an administrator must not be able to
// consent to a release this host never admitted. A helper that wrote only the
// target would therefore produce an uninstallable target and a confusing failure
// far from its cause.
func declarePresence(t *testing.T, bindingID, alias string) string {
	return declarePresenceKind(t, bindingID, alias, business.SolutionDeclaredKindSolution)
}

func declarePresenceKind(t *testing.T, bindingID, alias string, kind business.SolutionDeclaredKind) string {
	t.Helper()
	now := time.Now().UTC()
	var targetID string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		target, err := testStore.OpenSolutionTarget(ctx, bindingID, alias, 1, now)
		if err != nil {
			return err
		}
		targetID = target.ID
		return testStore.SaveSolutionHostBinding(ctx, appliedPresenceRecordKind(t, bindingID, alias, 1, now, false, string(kind)))
	}))
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		revision, err := testStore.NextSolutionRegistryRevision(ctx)
		if err != nil {
			return err
		}
		return testStore.SaveSolutionRegistration(ctx, &business.SolutionRegistration{
			SolutionID: alias, Publisher: "solution:" + alias, Revision: revision, UpdatedAt: now,
			Declared: &business.SolutionDeclaredBinding{BindingID: bindingID, TargetID: targetID, Generation: 1, Release: "acme/" + alias + "@1.0.0", Kind: kind},
		})
	}))
	return targetID
}

// appliedPresenceRecord is the binding row an applied generation leaves. The
// ownership domain is non-empty deliberately: core refuses an applied record
// without one, and an empty string passes a whole-or-absent constraint while
// being exactly the value it rejects — so a helper that left it blank would
// produce rows that store fine and freeze a reconciler.
func appliedPresenceRecord(
	t *testing.T, bindingID, alias string, generation uint64, now time.Time, removed bool,
) *business.SolutionHostBindingRecord {
	return appliedPresenceRecordKind(t, bindingID, alias, generation, now, removed, string(solutionhost.KindSolution))
}

func appliedPresenceRecordKind(t *testing.T, bindingID, alias string, generation uint64, now time.Time, removed bool, kind string) *business.SolutionHostBindingRecord {
	t.Helper()
	document := productionPathManifest(t, alias, generation)
	document.Kind = solutionhost.Kind(kind)
	document.Binding = bindingID
	document.Host = solutionhost.HostTarget{Coordinate: "acme/local/host", Domain: "alpha"}
	document.Release.Version = "1.0.0"
	if removed {
		document = productionPathWithdrawal(t, document, generation)
	}
	raw, err := document.CanonicalBytes()
	require.NoError(t, err)
	digest, err := document.Digest()
	require.NoError(t, err)
	entry := business.SolutionHostBindingGeneration{
		Generation: generation,
		Digest:     digest,
		Document:   string(raw),
		At:         now,
	}
	applied := &business.SolutionHostBindingApplied{
		SolutionHostBindingGeneration: entry,
		SolutionID:                    alias,
		Removed:                       removed,
		Domain:                        "alpha",
		Release:                       document.Release.Identity(),
	}
	if !removed {
		applied.Routes = []string{alias}
	}
	return &business.SolutionHostBindingRecord{
		BindingID:      bindingID,
		HostCoordinate: "acme/local/host",
		Desired:        &entry,
		Applied:        applied,
		UpdatedAt:      now,
	}
}
