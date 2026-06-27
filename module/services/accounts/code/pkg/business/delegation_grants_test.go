package business

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// =====================================================================
// Validation
// =====================================================================

func TestRequestDelegationInput_Validate_NilRejected(t *testing.T) {
	var in *RequestDelegationInput
	require.Error(t, in.Validate())
}

func TestRequestDelegationInput_Validate_EmptyOrg(t *testing.T) {
	in := &RequestDelegationInput{
		ActorPrincipalID: "p",
		Action:           "x",
		Justification:    "j",
		Timeout:          time.Minute,
	}
	err := in.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "org_id")
}

func TestRequestDelegationInput_Validate_EmptyActor(t *testing.T) {
	in := &RequestDelegationInput{
		OrgID:         "o",
		Action:        "x",
		Justification: "j",
		Timeout:       time.Minute,
	}
	err := in.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "actor_principal_id")
}

func TestRequestDelegationInput_Validate_EmptyAction(t *testing.T) {
	in := &RequestDelegationInput{
		OrgID:            "o",
		ActorPrincipalID: "p",
		Justification:    "j",
		Timeout:          time.Minute,
	}
	require.Error(t, in.Validate())
}

func TestRequestDelegationInput_Validate_BlankJustification(t *testing.T) {
	in := &RequestDelegationInput{
		OrgID:            "o",
		ActorPrincipalID: "p",
		Action:           "x",
		Justification:    "   \n  \t",
		Timeout:          time.Minute,
	}
	err := in.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "justification")
}

func TestRequestDelegationInput_Validate_ZeroTimeoutRejected(t *testing.T) {
	in := &RequestDelegationInput{
		OrgID:            "o",
		ActorPrincipalID: "p",
		Action:           "x",
		Justification:    "j",
	}
	require.Error(t, in.Validate())
}

func TestRequestDelegationInput_Validate_BadRiskLevel(t *testing.T) {
	in := &RequestDelegationInput{
		OrgID:            "o",
		ActorPrincipalID: "p",
		Action:           "x",
		Justification:    "j",
		Timeout:          time.Minute,
		RiskLevel:        "extreme",
	}
	err := in.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "risk_level")
}

func TestRequestDelegationInput_Validate_HappyPath(t *testing.T) {
	in := &RequestDelegationInput{
		OrgID:            "o",
		ActorPrincipalID: "p",
		Action:           "github.merge_pr",
		Resource:         "repo:foo",
		Justification:    "PR approved + CI green",
		RiskLevel:        RiskLevelMedium,
		Timeout:          5 * time.Minute,
	}
	require.NoError(t, in.Validate())
}

// =====================================================================
// Idempotency hash
// =====================================================================

func TestComputeRequestHash_DeterministicForSameInput(t *testing.T) {
	a := &RequestDelegationInput{
		OrgID: "o", ActorPrincipalID: "p",
		Action: "x", Resource: "r", Justification: "j",
	}
	b := &RequestDelegationInput{
		OrgID: "o", ActorPrincipalID: "p",
		Action: "x", Resource: "r", Justification: "j",
	}
	require.Equal(t, ComputeRequestHash(a), ComputeRequestHash(b),
		"same identity-fields → same hash")
}

func TestComputeRequestHash_DifferentForDifferentAction(t *testing.T) {
	a := &RequestDelegationInput{
		OrgID: "o", ActorPrincipalID: "p",
		Action: "x.read", Justification: "j",
	}
	b := &RequestDelegationInput{
		OrgID: "o", ActorPrincipalID: "p",
		Action: "x.write", Justification: "j",
	}
	require.NotEqual(t, ComputeRequestHash(a), ComputeRequestHash(b))
}

func TestComputeRequestHash_DifferentForDifferentJustification(t *testing.T) {
	a := &RequestDelegationInput{
		OrgID: "o", ActorPrincipalID: "p",
		Action: "x", Justification: "for thing 1",
	}
	b := &RequestDelegationInput{
		OrgID: "o", ActorPrincipalID: "p",
		Action: "x", Justification: "for thing 2",
	}
	require.NotEqual(t, ComputeRequestHash(a), ComputeRequestHash(b))
}

func TestComputeRequestHash_TolerantOfJustificationWhitespace(t *testing.T) {
	a := &RequestDelegationInput{
		OrgID: "o", ActorPrincipalID: "p",
		Action: "x", Justification: "  has trailing space  ",
	}
	b := &RequestDelegationInput{
		OrgID: "o", ActorPrincipalID: "p",
		Action: "x", Justification: "has trailing space",
	}
	require.Equal(t, ComputeRequestHash(a), ComputeRequestHash(b),
		"whitespace differences must not fork the hash — retries with messy justification still dedupe")
}

func TestComputeRequestHash_InsensitiveToContextChanges(t *testing.T) {
	// The Context map carries snapshot data (CI status, labels)
	// that an agent may pass differently across retries (clock-
	// dependent values). Two retries with different context but
	// same identity fields must still hash the same so they
	// dedupe.
	a := &RequestDelegationInput{
		OrgID: "o", ActorPrincipalID: "p",
		Action: "x", Justification: "j",
		Context: map[string]any{"snapshot_at": 1715300000},
	}
	b := &RequestDelegationInput{
		OrgID: "o", ActorPrincipalID: "p",
		Action: "x", Justification: "j",
		Context: map[string]any{"snapshot_at": 1715300100},
	}
	require.Equal(t, ComputeRequestHash(a), ComputeRequestHash(b))
}

func TestComputeRequestHash_ProducesHexEncodedSHA256(t *testing.T) {
	in := &RequestDelegationInput{
		OrgID: "o", ActorPrincipalID: "p",
		Action: "x", Justification: "j",
	}
	hash := ComputeRequestHash(in)
	require.Len(t, hash, 64, "SHA-256 hex is 64 chars")
	for _, c := range hash {
		require.True(t, strings.ContainsRune("0123456789abcdef", c),
			"only hex characters")
	}
}

// =====================================================================
// IsTerminal
// =====================================================================

func TestDelegationGrant_IsTerminal(t *testing.T) {
	cases := map[DelegationGrantStatus]bool{
		GrantStatusPending:   false,
		GrantStatusActive:    false, // pattern grants stay active
		GrantStatusApproved:  true,
		GrantStatusDenied:    true,
		GrantStatusExpired:   true,
		GrantStatusCancelled: true,
	}
	for status, want := range cases {
		t.Run(string(status), func(t *testing.T) {
			g := &DelegationGrant{Status: status}
			require.Equal(t, want, g.IsTerminal())
		})
	}
}
