package auth

import (
	"context"
	"reflect"
	"strings"
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

// An acting-as value over an actor that is not a canonical uuid must be refused
// outright. Admitting it would leave the target as the only usable id, and an
// identity naming one user is by definition not impersonating — so the target's
// own platform authority would resolve for a request the gateway said was a
// support session acting as them.
func TestParseRequestIdentityRefusesActingAsWithoutUsableActor(t *testing.T) {
	for _, actor := range []string{"not-a-uuid", "", uuid.Nil.String()} {
		_, err := ParseRequestIdentity(actor, identityTarget.String(), identityOrg.String(), "")
		require.ErrorIs(t, err, ErrRequestIdentityMalformed, actor)
	}
}

// The install point is the last line of defence for the same class: an identity
// carrying only one of the two ids never reaches a request context, so
// Impersonated() cannot be asked a question it would answer misleadingly.
func TestWithVerifiedRequestIdentityRefusesHalfFormedIdentity(t *testing.T) {
	halfFormed := []RequestIdentity{
		{EffectiveSubject: identityTarget},
		{RealActor: identityActor},
	}
	for _, identity := range halfFormed {
		ctx := WithVerifiedRequestIdentity(context.Background(), identity)
		_, ok := VerifiedRequestIdentity(ctx)
		require.False(t, ok, "half-formed identity must not install: %+v", identity)
		require.False(t, ImpersonatedRequest(ctx))
	}
}

// A registered client's id is carried as given. Empty is the host's own web
// session and is not an error, so the ordinary case needs no special handling
// downstream.
func TestParseClientIDCarriesARegisteredClient(t *testing.T) {
	client, err := ParseClientID("acme-console")
	require.NoError(t, err)
	require.Equal(t, "acme-console", client)

	absent, err := ParseClientID("")
	require.NoError(t, err)
	require.Empty(t, absent)
}

// A value that is not a client id is refused rather than dropped: dropping it
// would write "made from the host's own web session" into an append-only
// compliance record of a call that was not.
func TestParseClientIDRefusesAValueThatIsNotAClientID(t *testing.T) {
	for _, raw := range []string{
		"Acme-Console",  // the registry admits lowercase only
		"-leading-dash", // must start alphanumeric
		"a",             // shorter than the registry's minimum
		"acme console",  // no whitespace
		"acme/console",  // no path separators
		strings.Repeat("a", 65),
	} {
		_, err := ParseClientID(raw)
		require.ErrorIs(t, err, ErrRequestIdentityMalformed, "raw=%q", raw)
	}
}

// The in-process projection's client is the one line of this contract that
// could not be written when RequestIdentity.ClientID landed: it reads
// Identity.ClientID, which arrives with the registered-client sign-in on a
// different change. A compile-time reference would not build until then, so
// there was no way to guard the join — and a seam that two changes each half
// satisfy is exactly the kind that stays half-done silently.
//
// This guards it by reflection instead. It asserts nothing while Identity has
// no client, and fails the moment one exists without RequestIdentityOf
// projecting it.
func TestRequestIdentityOfProjectsTheTokensClientOnceIdentityCarriesOne(t *testing.T) {
	identity := &Identity{UserID: identityActor, OrgID: identityOrg}
	field := reflect.ValueOf(identity).Elem().FieldByName("ClientID")
	if !field.IsValid() {
		t.Skip("Identity carries no client yet; this guard arms when it does")
	}
	require.Equal(t, reflect.String, field.Kind(), "Identity.ClientID must stay a plain client id")
	field.SetString("example-console")

	require.Equal(t, "example-console", RequestIdentityOf(identity).ClientID,
		"Identity now carries a client but RequestIdentityOf drops it: a token presented "+
			"directly to accounts would record no client. Add `projected.ClientID = identity.ClientID`.")
}
