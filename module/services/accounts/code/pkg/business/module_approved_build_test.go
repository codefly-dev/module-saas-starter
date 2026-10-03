//go:build pure

package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The monotonicity contract, including the clause core's own MemorySealSource
// missed.

func digestA() ApprovedDigest { return ApprovedDigest(approvedBare) }
func digestB() ApprovedDigest {
	return ApprovedDigest("sha256:" +
		"3333333333333333333333333333333333333333333333333333333333333333")
}

// Clause 1: the incarnation never decreases.
func TestApprovedBuildIncarnationNeverDecreases(t *testing.T) {
	builds := NewMonotonicApprovedBuilds()
	require.NoError(t, builds.Approve("p", ApprovedBuildRecord{Digest: digestA(), Incarnation: 5}))

	err := builds.Approve("p", ApprovedBuildRecord{Digest: digestB(), Incarnation: 4})
	require.ErrorIs(t, err, ErrApprovedBuildRewind)

	// The refusal did not corrupt what is served.
	digest, incarnation, err := builds.ApprovedBuild(context.Background(), "p")
	require.NoError(t, err)
	require.Equal(t, digestA(), digest)
	require.Equal(t, uint64(5), incarnation)
}

// CLAUSE 2, and the case that matters: a swap-back at a fixed counter.
//
// Approve B at 5, then A again at 5. Clause 1 alone permits it — the incarnation
// did not decrease — and every capability sealed to A at 5, which the move to B
// revoked, verifies again. This is the rewind reached through the field the rule
// did not cover, and the reason the rule has two clauses.
func TestApprovedBuildSwapBackAtAFixedIncarnationIsRefused(t *testing.T) {
	builds := NewMonotonicApprovedBuilds()
	require.NoError(t, builds.Approve("p", ApprovedBuildRecord{Digest: digestA(), Incarnation: 5}))
	require.NoError(t, builds.Approve("p", ApprovedBuildRecord{Digest: digestB(), Incarnation: 6}))

	// Back to A, at B's incarnation. Not a decrease, and still a rewind.
	err := builds.Approve("p", ApprovedBuildRecord{Digest: digestA(), Incarnation: 6})
	require.ErrorIs(t, err, ErrApprovedBuildRewind)
	require.Contains(t, err.Error(), "without advancing incarnation")

	// Back to A WITH an advance is fine: that is a real new approval.
	require.NoError(t, builds.Approve("p", ApprovedBuildRecord{Digest: digestA(), Incarnation: 7}))
	digest, incarnation, err := builds.ApprovedBuild(context.Background(), "p")
	require.NoError(t, err)
	require.Equal(t, digestA(), digest)
	require.Equal(t, uint64(7), incarnation)
}

// Re-approving the SAME digest at the same incarnation is idempotent, not a
// rewind — a re-delivery of an unchanged document must not be refused.
func TestReApprovingTheSameBuildIsIdempotent(t *testing.T) {
	builds := NewMonotonicApprovedBuilds()
	require.NoError(t, builds.Approve("p", ApprovedBuildRecord{Digest: digestA(), Incarnation: 5}))
	require.NoError(t, builds.Approve("p", ApprovedBuildRecord{Digest: digestA(), Incarnation: 5}))
}

// The same content written as a bare digest and as a full reference is ONE
// digest, so a respelled re-delivery is not a change demanding a bump.
func TestTheSameDigestSpelledTwoWaysIsNotAChange(t *testing.T) {
	builds := NewMonotonicApprovedBuilds()
	require.NoError(t, builds.Approve("p", ApprovedBuildRecord{Digest: ApprovedDigest(approvedBare), Incarnation: 5}))
	require.NoError(t, builds.Approve("p", ApprovedBuildRecord{Digest: ApprovedDigest(approvedRef), Incarnation: 5}),
		"a respelling of the same content is not a move to a different image")
}

// The three states are distinct, and the distinction is the safety property.
//
// Collapsing "unknown" into "bears none" would mint an unbound capability for an
// identity the host has never heard of.
func TestApprovedBuildHasThreeDistinctStates(t *testing.T) {
	builds := NewMonotonicApprovedBuilds()

	_, _, err := builds.ApprovedBuild(context.Background(), "stranger")
	require.ErrorIs(t, err, ErrUnknownExecutionPrincipal)
	require.NotErrorIs(t, err, ErrNoApprovedBuild)

	builds.Declare("declared")
	_, _, err = builds.ApprovedBuild(context.Background(), "declared")
	require.ErrorIs(t, err, ErrNoApprovedBuild)
	require.NotErrorIs(t, err, ErrUnknownExecutionPrincipal)

	require.NoError(t, builds.Approve("approved", ApprovedBuildRecord{Digest: digestA(), Incarnation: 2}))
	digest, incarnation, err := builds.ApprovedBuild(context.Background(), "approved")
	require.NoError(t, err)
	require.Equal(t, digestA(), digest)
	require.Equal(t, uint64(2), incarnation)
}

// An empty authority means every principal is UNKNOWN, not that every principal
// bears no build — so a host that resolved nothing refuses rather than minting
// unbound capabilities for everyone.
func TestAnEmptyAuthorityRefusesRatherThanMintingUnbound(t *testing.T) {
	builds := NewMonotonicApprovedBuilds()
	_, _, err := builds.ApprovedBuild(context.Background(), "anyone")
	require.ErrorIs(t, err, ErrUnknownExecutionPrincipal)
}

// A non-digest cannot be approved: an approval is a statement about immutable
// content, and storing a tag now would serve a tag to every later check.
func TestANonDigestCannotBeApproved(t *testing.T) {
	builds := NewMonotonicApprovedBuilds()
	for _, value := range []string{"registry.example.com/acme/worker:v1", "", "latest", "sha256:short"} {
		err := builds.Approve("p", ApprovedBuildRecord{Digest: ApprovedDigest(value), Incarnation: 1})
		require.ErrorIs(t, err, ErrApprovedBuildRewind, "%q must not be approvable", value)
	}
}

// Incarnation 0 is refused here rather than at mint: core's schema requires
// gte=1 when the pair is present, so a zero would produce a seal its own
// validation rejects, far from where the value was written.
func TestIncarnationZeroIsRefused(t *testing.T) {
	builds := NewMonotonicApprovedBuilds()
	err := builds.Approve("p", ApprovedBuildRecord{Digest: digestA(), Incarnation: 0})
	require.ErrorIs(t, err, ErrApprovedBuildRewind)
}

// Principals are independent: one's incarnation says nothing about another's.
func TestPrincipalsAdvanceIndependently(t *testing.T) {
	builds := NewMonotonicApprovedBuilds()
	require.NoError(t, builds.Approve("a", ApprovedBuildRecord{Digest: digestA(), Incarnation: 9}))
	require.NoError(t, builds.Approve("b", ApprovedBuildRecord{Digest: digestB(), Incarnation: 1}),
		"a low incarnation for a different principal is not a rewind")
}
