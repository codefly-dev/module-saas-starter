package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var (
	identityActor  = uuid.MustParse("019f6bf7-5b1c-730d-9687-fe6d4aff31ee")
	identityTarget = uuid.MustParse("019f6bf7-5b4b-74e5-8c17-092259bb1663")
	identityOrg    = uuid.MustParse("019f6bf7-5b6d-7c02-93de-0f8f4a1cf0aa")
)

// An ordinary session answers both questions with the same user, so nothing
// downstream has to special-case the common case.
func TestRequestIdentityOfOrdinarySessionHasOneSubject(t *testing.T) {
	projected := RequestIdentityOf(&Identity{UserID: identityActor, OrgID: identityOrg})

	require.Equal(t, identityActor, projected.RealActor)
	require.Equal(t, identityActor, projected.EffectiveSubject)
	require.False(t, projected.Impersonated())
}

// An impersonation token authorizes as the target and stays attributable to the
// admin: `sub` is the actor and the `acting` claim is the effective subject.
func TestRequestIdentityOfImpersonationTokenSplitsActorFromSubject(t *testing.T) {
	projected := RequestIdentityOf(&Identity{
		UserID:         identityActor,
		ActingAsUserID: identityTarget,
		OrgID:          identityOrg,
	})

	require.Equal(t, identityActor, projected.RealActor)
	require.Equal(t, identityTarget, projected.EffectiveSubject)
	require.True(t, projected.Impersonated())
}

// The delegation chain is a different relationship and must not read as
// impersonation: the subject is still acting for themselves.
func TestRequestIdentityKeepsDelegationDistinctFromImpersonation(t *testing.T) {
	chain := &Actor{Subject: "svc:worker"}
	projected := RequestIdentityOf(&Identity{UserID: identityActor, Actor: chain})

	require.False(t, projected.Impersonated())
	require.Equal(t, chain, projected.Delegation)
}

func TestParseRequestIdentityProjectsForwardedFields(t *testing.T) {
	session := uuid.MustParse("019f6bf7-5b8e-7f31-a2b6-4c1de6b9f7c1")

	projected, err := ParseRequestIdentity(
		identityActor.String(), identityTarget.String(), identityOrg.String(), session.String())

	require.NoError(t, err)
	require.Equal(t, identityActor, projected.RealActor)
	require.Equal(t, identityTarget, projected.EffectiveSubject)
	require.Equal(t, identityOrg, projected.OrgID)
	require.Equal(t, session, projected.SessionID)
	require.True(t, projected.Impersonated())
}

// A garbled acting-as value must not degrade to "not impersonating": that would
// run a support session with the admin's own authority over the target's data.
func TestParseRequestIdentityRefusesMalformedActingAs(t *testing.T) {
	for _, actingAs := range []string{"not-a-uuid", uuid.Nil.String()} {
		_, err := ParseRequestIdentity(identityActor.String(), actingAs, identityOrg.String(), "")
		require.ErrorIs(t, err, ErrRequestIdentityMalformed, actingAs)
	}
}

// A principal that is not a canonical uuid yields no identity at all, so the
// request fails closed as unauthenticated rather than carrying a blank subject.
func TestParseRequestIdentityDropsNonCanonicalPrincipal(t *testing.T) {
	projected, err := ParseRequestIdentity("user-1", "", "org-1", "")
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, projected.EffectiveSubject)
	require.Empty(t, projected.EffectiveSubjectID())

	require.Nil(t, WithVerifiedRequestIdentity(context.Background(), projected).Value(verifiedRequestIdentityKey{}))
}

func TestVerifiedRequestIdentityRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, ok := VerifiedRequestIdentity(ctx)
	require.False(t, ok)
	require.False(t, ImpersonatedRequest(ctx))

	ctx = WithVerifiedRequestIdentity(ctx, RequestIdentity{
		RealActor:        identityActor,
		EffectiveSubject: identityTarget,
	})
	got, ok := VerifiedRequestIdentity(ctx)
	require.True(t, ok)
	require.Equal(t, identityActor.String(), got.RealActorID())
	require.Equal(t, identityTarget.String(), got.EffectiveSubjectID())
	require.True(t, ImpersonatedRequest(ctx))
}
