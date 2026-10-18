package auth_test

import (
	"testing"

	"accounts/pkg/auth"

	"github.com/stretchr/testify/require"
)

func TestOAuthErrorCodeReadsOnlyACodeShapedValue(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"rfc 6749 error member", `{"error":"invalid_grant","error_description":"code expired"}`, "invalid_grant"},
		{"client refusal", `{"error":"invalid_client"}`, "invalid_client"},
		{"code member when no error member", `{"code":"invalid_code","message":"x"}`, "invalid_code"},
		{"error member wins over code", `{"error":"invalid_client","code":"other"}`, "invalid_client"},
		{"non-string error falls back to code", `{"error":{"x":1},"code":"invalid_grant"}`, "invalid_grant"},
		{"not json", `<html>denied</html>`, ""},
		{"free text is not a code", `{"error":"The client secret sk_live_abc is wrong"}`, ""},
		{"hyphenated text is not a code", `{"error":"do-not-leak-this-provider-detail"}`, ""},
		{"upper case is not a code", `{"error":"INVALID_CLIENT"}`, ""},
		{"too long to be a code", `{"error":"invalid_grant_but_padded_well_past_forty_characters"}`, ""},
		{"no code at all", `{}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, auth.OAuthErrorCode([]byte(tc.body)))
		})
	}
}

func TestTokenEndpointErrorNamesStatusAndCodeOnly(t *testing.T) {
	require.Equal(t, "token endpoint answered http 400 (invalid_grant)",
		(&auth.TokenEndpointError{Status: 400, Code: "invalid_grant"}).Error())
	require.Equal(t, "token endpoint answered http 502",
		(&auth.TokenEndpointError{Status: 502}).Error())
}
