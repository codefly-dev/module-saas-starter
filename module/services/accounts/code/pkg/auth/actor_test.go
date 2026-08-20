package auth_test

import (
	"context"
	"strings"
	"testing"

	"accounts/pkg/auth"

	"github.com/stretchr/testify/require"
)

func TestValidateActorChain(t *testing.T) {
	require.NoError(t, auth.ValidateActorChain(nil), "no delegation is valid")
	require.NoError(t, auth.ValidateActorChain(&auth.Actor{
		Subject: "svc:billing-worker",
		Act:     &auth.Actor{Subject: "svc:gateway"},
	}))

	require.ErrorIs(t,
		auth.ValidateActorChain(&auth.Actor{Subject: "svc:a", Act: &auth.Actor{Subject: ""}}),
		auth.ErrActorSubjectMissing)

	var chain *auth.Actor
	for i := 0; i <= auth.MaxActorChainDepth; i++ {
		chain = &auth.Actor{Subject: "svc:x", Act: chain}
	}
	require.ErrorIs(t, auth.ValidateActorChain(chain), auth.ErrActorChainTooDeep)
}

func TestMarshalParseActorRoundTrip(t *testing.T) {
	chain := &auth.Actor{
		Subject: "svc:billing-worker",
		Act:     &auth.Actor{Subject: "svc:gateway"},
	}
	encoded, err := auth.MarshalActor(chain)
	require.NoError(t, err)
	require.Equal(t, `{"sub":"svc:billing-worker","act":{"sub":"svc:gateway"}}`, encoded)
	require.Equal(t, chain, auth.ParseActor(encoded))

	empty, err := auth.MarshalActor(nil)
	require.NoError(t, err)
	require.Empty(t, empty)
	require.Nil(t, auth.ParseActor(""))
}

func TestParseActorRejectsMalformedAndOversized(t *testing.T) {
	require.Nil(t, auth.ParseActor("not-json"))
	require.Nil(t, auth.ParseActor(`{"act":{"sub":""}}`), "blank leaf subject is rejected")

	// A hand-built over-deep JSON chain must not survive parsing.
	deep := strings.Repeat(`{"sub":"svc:x","act":`, auth.MaxActorChainDepth+1) +
		`null` + strings.Repeat("}", auth.MaxActorChainDepth+1)
	require.Nil(t, auth.ParseActor(deep))
}

func TestWithVerifiedActorContextRoundTrip(t *testing.T) {
	chain := &auth.Actor{Subject: "svc:billing-worker"}
	ctx := auth.WithVerifiedActor(context.Background(), chain)
	got, ok := auth.VerifiedActorFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, chain, got)

	// A nil chain leaves the context free of a recorded actor.
	_, ok = auth.VerifiedActorFromContext(auth.WithVerifiedActor(context.Background(), nil))
	require.False(t, ok)
}
