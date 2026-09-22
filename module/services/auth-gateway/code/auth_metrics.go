package main

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// Key-set and rejection labels are closed sets of compile-time constants, so
// the cardinality of these series is fixed however much untrusted traffic
// arrives. Nothing derived from a credential — no token, key id, subject, or
// session — is ever recorded.
const (
	jwksKeySetWorkContext = "work_context"
	jwksKeySetAccessToken = "access_token"

	jwtRejectionKeysUnavailable       = "keys_unavailable"
	jwtRejectionUnknownKeyID          = "unknown_key_id"
	jwtRejectionAmbiguousKeyID        = "ambiguous_key_id"
	jwtRejectionInvalidToken          = "invalid_token"
	jwtRejectionRevoked               = "revoked"
	jwtRejectionSessionRevoked        = "session_revoked"
	jwtRejectionRevocationUnavailable = "revocation_unavailable"
)

// Instruments are created once against the global meter provider. main installs
// the real provider only when observability is configured; before that (and in
// tests) the global provider is a no-op that discards these records, and the
// OTEL global forwards instruments created earlier once a provider arrives.
var authInstruments = sync.OnceValue(func() struct {
	refresh   otelmetric.Int64Counter
	rejection otelmetric.Int64Counter
} {
	meter := otel.Meter("github.com/codefly-dev/module-saas-starter/auth-gateway")
	refresh, _ := meter.Int64Counter(
		"auth_gateway.jwks.refresh",
		otelmetric.WithDescription("JWKS refresh attempts, by key set and outcome."),
	)
	rejection, _ := meter.Int64Counter(
		"auth_gateway.jwt.rejection",
		otelmetric.WithDescription("Access-token rejections, by cause."),
	)
	return struct {
		refresh   otelmetric.Int64Counter
		rejection otelmetric.Int64Counter
	}{refresh: refresh, rejection: rejection}
})

// recordJWKSRefresh reports the health of one key-set refresh.
func recordJWKSRefresh(keySet string, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	instruments := authInstruments()
	if instruments.refresh == nil {
		return
	}
	instruments.refresh.Add(context.Background(), 1, otelmetric.WithAttributes(
		attribute.String("key_set", keySet),
		attribute.String("outcome", outcome),
	))
}

// recordJWTRejection reports why an access token was refused.
func recordJWTRejection(ctx context.Context, reason string) {
	instruments := authInstruments()
	if instruments.rejection == nil {
		return
	}
	instruments.rejection.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("reason", reason)))
}
