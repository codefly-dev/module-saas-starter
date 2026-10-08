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
// The organization is passed EXPLICITLY rather than read from the context. An
// implicit read that returned "" would seal a customer's credential under this
// deployment's key — silently, and exactly for the customer who went to the
// trouble of bringing their own. That is the failure this type exists to
// prevent, so the argument is required and an empty one is refused.
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
	if orgID == "" {
		return "", errors.New("key service: opening an organization's secret requires the organization")
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
	if orgID == "" {
		return nil, errors.New("key service: sealing an organization's secret requires the organization")
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
