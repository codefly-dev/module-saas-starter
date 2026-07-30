package infra

import (
	"context"
	"encoding/json"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

func TestWebAuthnEngineEmitsPasskeyAndUserVerificationPolicy(t *testing.T) {
	engine, err := NewWebAuthnEngine("example.com", "Example", []string{"https://app.example.com"})
	require.NoError(t, err)

	options, session, expiresAt, err := engine.BeginRegistration(context.Background(), business.WebAuthnUser{
		ID:          []byte("stable-user-id"),
		Name:        "alex@example.com",
		DisplayName: "Alex",
	})
	require.NoError(t, err)
	require.NotEmpty(t, session)
	require.False(t, expiresAt.IsZero())

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(options, &decoded))
	require.NotContains(t, decoded, "publicKey", "browser client receives the raw options object")
	authenticatorSelection, ok := decoded["authenticatorSelection"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "preferred", authenticatorSelection["residentKey"])
	require.Equal(t, "required", authenticatorSelection["userVerification"])
	require.NotEmpty(t, decoded["challenge"])
}

func TestWebAuthnEngineRejectsIncompleteRelyingPartyConfig(t *testing.T) {
	_, err := NewWebAuthnEngine("", "Example", []string{"https://app.example.com"})
	require.Error(t, err)
	_, err = NewWebAuthnEngine("example.com", "Example", nil)
	require.NoError(t, err, "Codefly supplies the exact browser origin at request time")
}

func TestWebAuthnEngineUsesVerifiedCodeflyOrigin(t *testing.T) {
	engine, err := NewWebAuthnEngine("localhost", "Example", nil)
	require.NoError(t, err)
	ctx, err := auth.WithVerifiedPublicOrigin(context.Background(), "http://localhost:54321")
	require.NoError(t, err)

	options, _, _, err := engine.BeginRegistration(ctx, business.WebAuthnUser{
		ID:          []byte("stable-user-id"),
		Name:        "alex@example.com",
		DisplayName: "Alex",
	})
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(options, &decoded))
	rp := decoded["rp"].(map[string]any)
	require.Equal(t, "localhost", rp["id"])
}
