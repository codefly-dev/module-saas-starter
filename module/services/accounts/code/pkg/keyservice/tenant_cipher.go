package keyservice

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// TenantCipher seals an organization's credentials under that organization's own
// key when it has one, and under the deployment's key when it does not.
//
// Having none is the default and stays the default: "the customer does not care"
// is the common case, and a deployment that had to provision a key per
// organization before it could store anything would be worse for everyone. A
// binding is an OVERRIDE.
//
// Which organization a value belongs to comes from the ROW being sealed —
// sub.OrgID, source.OrgID, the org a provider is configured for — not from the
// request context, and not threaded down from a caller's caller. Two reasons it
// cannot be the context:
//
//   - the delivery paths have none. The outbound webhook sender opens a
//     subscription's signing secret as app_webhook_worker, outside any tenant
//     transaction, so there is no verified organization to read; the row is all
//     there is.
//   - the key should follow the DATA. A value's organization is a property of
//     the row, and sealing it under whatever organization the current request
//     happens to be scoped to would be a different question with the same
//     answer most of the time, which is the worst kind of coincidence.
//
// Where the context DOES carry a verified organization, the two must agree, and
// requireConsistentScope refuses when they do not: the row's organization and
// the transaction's scope disagreeing means one of them is wrong, and guessing
// which seals a customer's credential under another customer's key. Row-level
// security binds the transaction, never this argument, so nothing else catches
// it.
//
// An absent organization is refused outright. A fallback to the deployment's key
// would betray exactly the customer who went to the trouble of bringing their
// own.
type TenantCipher struct {
	// deployment seals for an organization with no key of its own, and is the
	// only thing that computes the keyed hash.
	deployment *Cipher
	factory    KeyFactory
	bindings   OrgKeyBindings

	// sealers caches by key reference, not by organization: two organizations
	// pointed at one key share a sealer, and building one is a round trip to the
	// key service (Cloud KMS validates and round-trips the key), far too much to
	// repeat per request.
	mu      sync.RWMutex
	sealers map[string]Sealer
}

func NewTenantCipher(deployment *Cipher, factory KeyFactory, bindings OrgKeyBindings) (*TenantCipher, error) {
	if deployment == nil || factory == nil || bindings == nil {
		return nil, errors.New("key service: a tenant cipher needs the deployment cipher, a key factory and a binding source")
	}
	return &TenantCipher{
		deployment: deployment, factory: factory, bindings: bindings,
		sealers: map[string]Sealer{},
	}, nil
}

// EncryptSecret and DecryptSecret keep the deployment-wide behaviour for values
// that are not an organization's — a person's MFA seed and WebAuthn credential,
// which belong to someone who may be in many organizations.
func (c *TenantCipher) EncryptSecret(ctx context.Context, purpose, plaintext string) (string, error) {
	return c.deployment.EncryptSecret(ctx, purpose, plaintext)
}

func (c *TenantCipher) DecryptSecret(ctx context.Context, purpose, stored string) (string, error) {
	return c.deployment.DecryptSecret(ctx, purpose, stored)
}

// HashKey is always the deployment's: API-key lookup is BY the hash, so a hash
// computed under an organization's key could never be found — the lookup is what
// discovers which organization the key belongs to.
func (c *TenantCipher) HashKey(ctx context.Context, plaintext string) (string, error) {
	return c.deployment.HashKey(ctx, plaintext)
}

func (c *TenantCipher) CandidateHashes(ctx context.Context, plaintext string) ([]string, error) {
	return c.deployment.CandidateHashes(ctx, plaintext)
}

// EncryptTenantSecret seals under the organization's key, or the deployment's
// when it has none.
func (c *TenantCipher) EncryptTenantSecret(ctx context.Context, orgID, purpose, plaintext string) (string, error) {
	sealer, err := c.sealerFor(ctx, orgID)
	if err != nil {
		return "", err
	}
	if sealer == nil {
		return c.deployment.EncryptSecret(ctx, purpose, plaintext)
	}
	if purpose == "" || plaintext == "" {
		return "", errors.New("key service: sealing requires a purpose and a plaintext")
	}
	payload, err := sealer.Seal(ctx, purpose, plaintext)
	if err != nil {
		return "", err
	}
	return Envelope{Backend: sealer.Tag(), Payload: payload}.String(), nil
}

// DecryptTenantSecret opens a value through whichever bound key sealed it: the
// organization's own, or the deployment's.
//
// A value is routed by what the ENVELOPE names, not by what the binding says
// now, because the two legitimately disagree during a cutover — an organization
// that has just been given its own key still holds credentials sealed under the
// deployment's, and those must keep opening until they are re-sealed.
func (c *TenantCipher) DecryptTenantSecret(ctx context.Context, orgID, purpose, stored string) (string, error) {
	if err := requireConsistentScope(ctx, orgID, "opening"); err != nil {
		return "", err
	}
	binding, err := c.bindings.OrgKeyBinding(ctx, orgID)
	if err != nil {
		return "", fmt.Errorf("key service: read the organization's key binding: %w", err)
	}
	envelope, err := ParseEnvelope(stored)
	if err != nil {
		return "", err
	}
	if binding != nil {
		if binding.Revoked() {
			// Named, and NOT an invalid envelope: this credential is unreadable
			// because the customer's key is gone, which is the outcome they
			// asked for. Reporting it as a broken credential would tell them to
			// reconnect the source whose data they destroyed.
			return "", fmt.Errorf("%w (%s): the credentials sealed under it cannot be read", ErrKeyRevoked, binding.RevokedReason)
		}
		sealer, err := c.boundSealer(ctx, binding.KeyRef)
		if err != nil {
			return "", err
		}
		if sealer.Accepts(envelope) {
			return sealer.Open(ctx, purpose, envelope.Payload)
		}
	}
	// Sealed under the deployment's key — either the organization has no key of
	// its own, or it has one and this value predates it.
	return c.deployment.DecryptSecret(ctx, purpose, stored)
}

// ResealTenantSecret moves one value onto the organization's current key. A value
// already under it is returned unchanged with false, so a sweep is restart-safe.
func (c *TenantCipher) ResealTenantSecret(ctx context.Context, orgID, purpose, stored string) (string, bool, error) {
	sealer, err := c.sealerFor(ctx, orgID)
	if err != nil {
		return "", false, err
	}
	envelope, err := ParseEnvelope(stored)
	if err != nil {
		return "", false, err
	}
	target := c.deployment.seal
	if sealer != nil {
		target = sealer
	}
	if target.Accepts(envelope) {
		return stored, false, nil
	}
	plaintext, err := c.DecryptTenantSecret(ctx, orgID, purpose, stored)
	if err != nil {
		return "", false, err
	}
	resealed, err := c.EncryptTenantSecret(ctx, orgID, purpose, plaintext)
	if err != nil {
		return "", false, err
	}
	return resealed, true, nil
}

// sealerFor resolves the organization's sealer, or nil when it has none and the
// deployment's key applies.
func (c *TenantCipher) sealerFor(ctx context.Context, orgID string) (Sealer, error) {
	if err := requireConsistentScope(ctx, orgID, "sealing"); err != nil {
		return nil, err
	}
	binding, err := c.bindings.OrgKeyBinding(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("key service: read the organization's key binding: %w", err)
	}
	if binding == nil {
		return nil, nil
	}
	if binding.Revoked() {
		return nil, fmt.Errorf("%w (%s): nothing further may be sealed under it", ErrKeyRevoked, binding.RevokedReason)
	}
	return c.boundSealer(ctx, binding.KeyRef)
}

func (c *TenantCipher) boundSealer(ctx context.Context, keyRef string) (Sealer, error) {
	c.mu.RLock()
	cached, ok := c.sealers[keyRef]
	c.mu.RUnlock()
	if ok {
		return cached, nil
	}
	sealer, err := c.factory.SealerFor(ctx, keyRef)
	if err != nil {
		return nil, fmt.Errorf("key service: bind the organization's key: %w", err)
	}
	c.mu.Lock()
	// Another goroutine may have won the race; keep one sealer per key so the
	// cache cannot grow a second entry for the same reference.
	if existing, raced := c.sealers[keyRef]; raced {
		sealer = existing
	} else {
		c.sealers[keyRef] = sealer
	}
	c.mu.Unlock()
	return sealer, nil
}

// VerifiedScope reports the organization the caller's transaction is scoped to,
// when it is running inside one. A path with no request identity — a delivery
// worker — reports false, which is not an error.
//
// It is a variable so the wiring supplies it: this package must not import the
// request-identity plumbing, and a test must be able to drive both answers.
var VerifiedScope func(ctx context.Context) (orgID string, ok bool)

// requireConsistentScope refuses a row whose organization is absent, and one
// that disagrees with the transaction's own scope.
func requireConsistentScope(ctx context.Context, orgID, verb string) error {
	if orgID == "" {
		return fmt.Errorf("key service: %s an organization's secret requires the organization it belongs to", verb)
	}
	if VerifiedScope == nil {
		return nil
	}
	scope, ok := VerifiedScope(ctx)
	if !ok || scope == orgID {
		// No request scope is the delivery-worker case, which is legitimate.
		return nil
	}
	return fmt.Errorf("key service: refusing to %s a secret for organization %s inside a transaction scoped to %s: the row and the scope disagree, and row-level security binds the scope rather than this argument, so one of them is wrong",
		verb, orgID, scope)
}
