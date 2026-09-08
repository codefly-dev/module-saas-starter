package adapters

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Module-registration credential exchange.
//
// A composed module federates its REST prefix with the gateway by presenting a
// signed, prefix-bound registration token (auth-gateway's
// X-Codefly-Module-Registration). accounts is the authority that issues it:
// the gateway already trusts this service's Ed25519 key through JWKS, so
// signing here needs no second trust root, and the decision "may this caller
// own that prefix" is made server-side by the one service that holds the
// answer instead of being asserted by the registrant.
//
// The caller authenticates twice, and both checks fail closed:
//
//   - the cluster-internal token, which places the caller inside the mesh; and
//   - the module's own registration secret, which says WHICH module it is.
//
// The second is what the shared secret alone could never give: a module holding
// the credential for "documents" cannot obtain a token for "billing", because
// the secret is compared against the entry configured for the prefix it asks
// for. Composition provisions the pair — the SHA-256 digest here, the
// plaintext in the module's own configuration — so the binding is declared once,
// at composition time, and a running module can never widen it.

// ModuleRegistrationPath is served on the private REST listener only. It is
// absent from the gateway route catalog, so the edge cannot reach it; the
// gateway brokers the exchange for a module over its internal channel.
const ModuleRegistrationPath = "/internal/module-registration/token"

// moduleRegistrationInternalTokenHeader carries the cluster-internal
// credential, the same one every other internal-authority call presents.
const moduleRegistrationInternalTokenHeader = "X-Codefly-Internal-Token"

// modulePrefixPattern mirrors the gateway's catalog-identity rule: one lowercase
// DNS-ish segment, no slashes, wildcards, or traversal. Validating here too
// keeps a malformed prefix out of a signed claim rather than relying on the
// verifier to catch it later.
var modulePrefixPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

// moduleRegistrationMinter is deliberately narrower than auth.JWTMinter: issuing
// a registration credential needs the signing key and nothing else — no session
// store, no refresh rotation, no identity.
type moduleRegistrationMinter interface {
	MintModuleRegistration(prefix string) (string, time.Time, error)
}

// ParseModuleRegistrationSecrets reads the composition-declared registration
// credentials, formatted as comma-separated `prefix:sha256hex` entries. Only
// the digest lives here; the module holds the plaintext.
//
// An empty value yields an empty map, which denies every registration: a
// deployment that has not declared who may federate must not let anyone.
func ParseModuleRegistrationSecrets(raw string) (map[string][sha256.Size]byte, error) {
	secrets := map[string][sha256.Size]byte{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		prefix, digest, ok := strings.Cut(entry, ":")
		if !ok {
			return nil, fmt.Errorf("module registration secret %q is not prefix:sha256hex", entry)
		}
		prefix = strings.TrimSpace(prefix)
		if !modulePrefixPattern.MatchString(prefix) || len(prefix) > 63 {
			return nil, fmt.Errorf("module registration secret has invalid prefix %q", prefix)
		}
		decoded, err := hex.DecodeString(strings.TrimSpace(digest))
		if err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("module registration secret for %q is not a sha256 hex digest", prefix)
		}
		if _, duplicate := secrets[prefix]; duplicate {
			return nil, fmt.Errorf("module registration secret for %q declared twice", prefix)
		}
		secrets[prefix] = [sha256.Size]byte(decoded)
	}
	return secrets, nil
}

// NewModuleRegistrationHTTPHandler serves the credential exchange. secrets maps
// a module prefix to the SHA-256 digest of that module's registration secret.
func NewModuleRegistrationHTTPHandler(minter moduleRegistrationMinter, secrets map[string][sha256.Size]byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ModuleRegistrationPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !validInternalToken(r.Header.Get(moduleRegistrationInternalTokenHeader)) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		var payload struct {
			Prefix string `json:"prefix"`
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&payload); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		// One outcome for "no such prefix" and "wrong secret": a caller probing
		// the exchange learns nothing about which modules a composition declared.
		expected, declared := secrets[payload.Prefix]
		presented := sha256.Sum256([]byte(payload.Secret))
		if !declared || payload.Secret == "" ||
			subtle.ConstantTimeCompare(expected[:], presented[:]) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		token, expiresAt, err := minter.MintModuleRegistration(payload.Prefix)
		if err != nil {
			http.Error(w, "Registration token unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token":     token,
			"expiresAt": expiresAt.UTC().Format(time.RFC3339),
		})
	})
}
