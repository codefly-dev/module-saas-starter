package adapters

import (
	"context"
	"encoding/json"
	"net/http"
)

const jwksPath = "/v1/auth/.well-known/jwks.json"

// jwksProvider is deliberately narrower than business.Service. The public
// standards endpoint only needs the cluster's current verification keys.
type jwksProvider interface {
	GetJWKS(context.Context) (string, error)
}

// NewJWKSHTTPHandler exposes the exact JSON Web Key Set rather than the
// grpc-gateway representation of JWKSResponse:
//
//	{"keysJson":"{\"keys\":[...]}"}
//
// Consumers expect the standard top-level {"keys":[...]} document. The
// protobuf RPC remains available to typed gRPC/Connect clients; this handler
// owns only the well-known HTTP shape.
func NewJWKSHTTPHandler(provider jwksProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != jwksPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		raw, err := provider.GetJWKS(r.Context())
		if err != nil {
			http.Error(w, "JWKS unavailable", http.StatusServiceUnavailable)
			return
		}
		var document struct {
			Keys []json.RawMessage `json:"keys"`
		}
		if err := json.Unmarshal([]byte(raw), &document); err != nil || len(document.Keys) == 0 {
			http.Error(w, "JWKS unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(raw))
	})
}
