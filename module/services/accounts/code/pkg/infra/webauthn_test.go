package infra

import (
	"context"
	"encoding/json"
	"testing"

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
	require.Error(t, err)
}
