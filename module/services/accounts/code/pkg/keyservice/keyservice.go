// Package keyservice is the seam between accounts and whatever holds its keys.
//
// accounts holds exactly two: the Ed25519 signing key behind every access token
// and the published JWKS, and the envelope key that seals stored credentials —
// source tokens, MFA seeds, WebAuthn credentials, webhook secrets — together
// with the keyed hash behind every API key.
//
// Which service holds them is a DEPLOYMENT decision, selected by configuration
// and never inferred. An in-cluster deployment names `vault` and keeps the
// AppRole + Transit binding. A hosted deployment names `kms` and uses the
// cell's cloud key-management service: keys that cannot be exported, reached
// with the workload's own cloud identity, so there is no stateful secrets store
// to run, unseal and back up, and no stored credential to rotate — the
// configuration carries key *names* and nothing else.
//
// The two families are separate interfaces because a backend can hold one key
// and not the other, and because they fail differently: a signing key that
// cannot be reached stops new logins, while an envelope key that cannot be
// reached stops reading credentials that already exist.
package keyservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Backend is the configured key service. It has no usable zero value: an
// unstated backend is refused rather than defaulted, because the two shapes
// have opposite custody models and a deployment that inherited one by accident
// would be reaching a key service nobody chose for it.
type Backend string

const (
	// BackendVault is HashiCorp Vault: KV v2 for the signing key, Transit for
	// the envelope key and the keyed hash, reached with an AppRole credential.
	BackendVault Backend = "vault"
	// BackendKMS is the deployment's cloud key-management service, reached with
	// the workload's cloud identity. The key is never exportable: every
	// operation is a request.
	BackendKMS Backend = "kms"
	// BackendKMSWrapped holds a key as a ciphertext the cloud key service can
	// unwrap, rather than as a key the service operates on.
	//
	// It is a SEPARATE backend rather than a fallback inside BackendKMS because
	// it is a weaker property and an operator must choose it knowingly: the
	// unwrapped key exists in process memory, so it is non-exportable at rest
	// and not non-exportable in use. Collapsing the two would make the weaker
	// one the state a deployment lands in by accident.
	BackendKMSWrapped Backend = "kms-wrapped"
)

// Backends is every backend a deployment may select, in the order a refusal
// lists them.
var Backends = []Backend{BackendVault, BackendKMS, BackendKMSWrapped}

// ParseBackend resolves a configured value, naming what was wrong.
func ParseBackend(value string) (Backend, error) {
	switch candidate := Backend(strings.TrimSpace(value)); candidate {
	case BackendVault, BackendKMS, BackendKMSWrapped:
		return candidate, nil
	case "":
		return "", errors.New("no key-service backend is selected")
	default:
		return "", fmt.Errorf("unsupported key-service backend %q: use %s", value, joinBackends())
	}
}

func joinBackends() string {
	names := make([]string, 0, len(Backends))
	for _, backend := range Backends {
		names = append(names, string(backend))
	}
	return strings.Join(names, " or ")
}

// Sealer is one backend's stored-credential family: envelope encryption with
// associated data, and the keyed hash.
//
// Seal and Open exchange the backend's own payload — the part of a stored
// envelope after its tag — rather than the whole envelope. Framing a value and
// routing it back to the backend that sealed it belong to one place (Envelope
// and Cipher); a backend only has to be able to seal and open its own.
type Sealer interface {
	// Tag names this backend in every envelope it seals.
	Tag() string
	// Identity distinguishes this backend from every other one a deployment
	// could bind, key included. Two Cloud KMS keys share a tag but not an
	// identity, which is what makes a key-to-key cutover expressible.
	Identity() string
	// Accepts reports whether this backend sealed that envelope — the tag, and
	// for a backend whose key is named in configuration, the key as well.
	//
	// Routing on the tag alone would be wrong: two Cloud KMS keys share the tag
	// `gcp-kms`, so a deployment re-keying from one to another would hand each
	// ciphertext to whichever backend was asked first, and Cloud KMS answers a
	// ciphertext from a foreign key with the same 400 it answers a corrupt one.
	Accepts(envelope Envelope) bool
	// Seal binds purpose into the ciphertext as associated data, so a payload
	// moved between rows no longer opens. It returns the payload only; the
	// caller frames it.
	Seal(ctx context.Context, purpose, plaintext string) (payload string, err error)
	// Open reverses Seal. It refuses a payload sealed under a different
	// purpose, and never falls back to returning the ciphertext.
	Open(ctx context.Context, purpose, payload string) (plaintext string, err error)
	// MAC is the keyed hash behind API-key lookup. It is deterministic: the
	// same plaintext under the same key version is always the same string, and
	// the stored hash is what a presented key is looked up by.
	MAC(ctx context.Context, plaintext string) (string, error)
}

// statusError preserves a backend's HTTP status without retaining its response
// body. A key service's error body quotes the request — Vault reflects the
// token's own path, Cloud KMS echoes request fields — so only the status
// travels, and callers that need to tell an outage from a refusal read it back
// through HTTPStatusCode.
type statusError struct {
	service string
	status  int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s request returned %d", e.service, e.status)
}
func (e *statusError) HTTPStatusCode() int { return e.status }

// KeyFactory binds another key of the same key service, named by a reference the
// platform recorded for an organization.
//
// The reference is OPAQUE above this interface: only the backend knows whether
// it is a Vault transit key name or a cloud resource name, which is what keeps
// the vendor out of the caller and out of the database.
type KeyFactory interface {
	SealerFor(ctx context.Context, keyRef string) (Sealer, error)
}

// OrgKeyBinding is what the platform recorded about an organization's key.
type OrgKeyBinding struct {
	// KeyRef is the key, as the bound backend names one.
	KeyRef string
	// CustomerHeld marks a key this deployment does not control.
	CustomerHeld bool
	// RevokedReason is set once the key is gone — destroyed on request, or
	// revoked by the customer. Every credential sealed under it is unreadable
	// from that moment, which is the point.
	RevokedReason string
}

// Revoked reports whether the key is gone.
func (b OrgKeyBinding) Revoked() bool { return b.RevokedReason != "" }

// OrgKeyBindings answers which key seals an organization's credentials. A nil
// binding means the organization has none of its own and is sealed under the
// deployment's key, which is the default and stays the default.
type OrgKeyBindings interface {
	OrgKeyBinding(ctx context.Context, orgID string) (*OrgKeyBinding, error)
}

// ErrKeyRevoked reports that an organization's key is gone, so its credentials
// are unreadable by design.
//
// It is deliberately NOT business.ErrInvalidSecretEnvelope: callers treat that
// sentinel as a credential the user should re-enter, and telling a customer to
// reconnect a source whose data they instructed us to destroy is the opposite of
// what crypto-shredding is for.
var ErrKeyRevoked = errors.New("key service: the organization's key has been revoked")
