package auth_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
)

func TestVerifiedPublicOriginContext(t *testing.T) {
	ctx, err := auth.WithVerifiedPublicOrigin(context.Background(), "http://localhost:54321/")
	require.NoError(t, err)
	origin, ok := auth.VerifiedPublicOrigin(ctx)
	require.True(t, ok)
	require.Equal(t, "http://localhost:54321", origin)
}

func TestCanonicalPublicOriginRejectsUnsafeValues(t *testing.T) {
	for _, candidate := range []string{
		"",
		"localhost:54321",
		"http://app.example.com",
		"https://user@app.example.com",
		"https://app.example.com/base",
		"https://app.example.com?tenant=x",
		"https://app.example.com#fragment",
	} {
		_, err := auth.CanonicalPublicOrigin(candidate)
		require.Error(t, err, candidate)
	}
}
