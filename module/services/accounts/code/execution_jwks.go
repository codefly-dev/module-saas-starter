package main

import (
	"accounts/pkg/adapters"
	"accounts/pkg/auth"
	"context"
	"net/http"
)

// Reuse the normal signer's public verification set, including configured
// rotation overlap keys. Never derive a separate key set from the custody key.
type executionSigningKeys struct{ auth.JWTMinter }

func (p executionSigningKeys) GetJWKS(context.Context) (string, error) {
	return p.JWKS()
}

func mountExecutionJWKS(broker *http.Server, minter auth.JWTMinter) {
	custody := broker.Handler
	jwks := adapters.NewJWKSHTTPHandler(executionSigningKeys{minter})
	broker.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/.well-known/jwks.json" {
			jwks.ServeHTTP(w, r)
			return
		}
		custody.ServeHTTP(w, r)
	})
}
