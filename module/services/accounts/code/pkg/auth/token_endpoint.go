package auth

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// TokenEndpointError is an identity provider's refusal of an authorization-code
// exchange, kept to what is safe to carry anywhere: the HTTP status and the OAuth
// error code (RFC 6749 §5.2) the response named. The response body is never
// kept — its description is free text the provider writes, and an error value
// travels to logs, to the login response and to diagnostics alike.
type TokenEndpointError struct {
	Status int
	// Code is the response's error code, or "" when it named none that reads as
	// one (see OAuthErrorCode).
	Code string
}

func (e *TokenEndpointError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("token endpoint answered http %d", e.Status)
	}
	return fmt.Sprintf("token endpoint answered http %d (%s)", e.Status, e.Code)
}

// oauthErrorCode is the shape every registered OAuth error code has
// (`invalid_grant`, `invalid_client`, …): lower-case ASCII words joined by
// underscores. A value of any other shape is not passed on — the bound is what
// keeps a provider's free text out of the code.
var oauthErrorCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// OAuthErrorCode reads the error code from a token endpoint's error response:
// the RFC 6749 `error` member, or the `code` member some providers use instead.
// It returns "" when the body is not JSON or the value is not code-shaped.
func OAuthErrorCode(body []byte) string {
	var parsed struct {
		Error any `json:"error"`
		Code  any `json:"code"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	for _, value := range []any{parsed.Error, parsed.Code} {
		if code, ok := value.(string); ok && oauthErrorCode.MatchString(code) {
			return code
		}
	}
	return ""
}
