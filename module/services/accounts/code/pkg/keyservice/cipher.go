package keyservice

import (
	"context"
	"errors"
	"fmt"
)

// Cipher is the key service as accounts' stored-credential callers see it: one
// backend that seals, and — during a cutover — the backend being migrated away
// from, accepted on read only.
//
// It implements business.SecretCipher and business.KeyHasher, so selecting a
// backend changes nothing above this type.
type Cipher struct {
	seal     Sealer
	previous Sealer
}

// NewCipher builds the cipher over the backend that seals and, when a cutover
// is in progress, the one being migrated away from. previous may be nil.
func NewCipher(seal, previous Sealer) (*Cipher, error) {
	if seal == nil {
		return nil, errors.New("key service: a sealing backend is required")
	}
	// Two backends sharing a TAG are fine — a re-key from one Cloud KMS key to
	// another is a real cutover, and the envelope names the key so reads route
	// correctly. What is refused is a previous backend that would accept the
	// very envelopes the selected one writes: nothing would ever be re-sealed,
	// and the sweep would report progress while doing nothing.
	if previous != nil && previous.Identity() == seal.Identity() {
		return nil, fmt.Errorf("key service: the previous backend is the same key as the selected one (%s); a cutover has two distinct backends or none, and naming one twice would make the sweep that re-seals every stored credential a no-op while reading as progress", seal.Identity())
	}
	return &Cipher{seal: seal, previous: previous}, nil
}

// SealsWith is the tag every value this cipher seals is stored under. A
// migration sweep uses it to recognise what it has already done.
func (c *Cipher) SealsWith() string { return c.seal.Tag() }

// PreviousTag is the tag the cutover is migrating away from, or "" when no
// cutover is configured.
func (c *Cipher) PreviousTag() string {
	if c.previous == nil {
		return ""
	}
	return c.previous.Tag()
}

// EncryptSecret seals a purpose-bound value under the selected backend. There
// is no plaintext fallback: a credential that failed to seal is not stored.
func (c *Cipher) EncryptSecret(ctx context.Context, purpose, plaintext string) (string, error) {
	if purpose == "" || plaintext == "" {
		return "", errors.New("key service: sealing requires a purpose and a plaintext")
	}
	payload, err := c.seal.Seal(ctx, purpose, plaintext)
	if err != nil {
		return "", err
	}
	return Envelope{Backend: c.seal.Tag(), Payload: payload}.String(), nil
}

// DecryptSecret opens a stored envelope through whichever configured backend
// its tag names. A tag no backend answers for is refused by name: that is the
// signal a cutover is incomplete, and it is strictly better than the
// alternative of handing the bytes to the wrong backend.
func (c *Cipher) DecryptSecret(ctx context.Context, purpose, stored string) (string, error) {
	if purpose == "" {
		return "", errors.New("key service: opening requires a purpose")
	}
	envelope, err := ParseEnvelope(stored)
	if err != nil {
		return "", err
	}
	backend, err := c.readerFor(envelope)
	if err != nil {
		return "", err
	}
	return backend.Open(ctx, purpose, envelope.Payload)
}

// readerFor picks the backend that actually sealed the envelope. It asks each
// bound backend rather than matching the tag, because two backends can share a
// tag and differ in the key they hold, and handing a ciphertext to the wrong one
// of those is indistinguishable from a corrupt ciphertext at the key service.
func (c *Cipher) readerFor(envelope Envelope) (Sealer, error) {
	if c.seal.Accepts(envelope) {
		return c.seal, nil
	}
	if c.previous != nil && c.previous.Accepts(envelope) {
		return c.previous, nil
	}
	return nil, fmt.Errorf("stored secret was sealed by %q, which this deployment does not bind: select it as the previous key-service backend until nothing references it", envelope.Backend)
}

// Reseal opens a stored value and seals it again under the selected backend.
// A value already sealed by the selected backend is returned unchanged with
// false, so a sweep is restart-safe and re-running it is free.
func (c *Cipher) Reseal(ctx context.Context, purpose, stored string) (string, bool, error) {
	envelope, err := ParseEnvelope(stored)
	if err != nil {
		return "", false, err
	}
	if c.seal.Accepts(envelope) {
		return stored, false, nil
	}
	plaintext, err := c.DecryptSecret(ctx, purpose, stored)
	if err != nil {
		return "", false, err
	}
	resealed, err := c.EncryptSecret(ctx, purpose, plaintext)
	if err != nil {
		return "", false, err
	}
	return resealed, true, nil
}

// HashKey is the hash a newly issued API key is stored under.
//
// There is intentionally no local fallback: silently downgrading to an unkeyed
// digest would produce a hash that never matches the one written for the same
// key once the key service recovers, so verification would break for every key
// created during the outage.
func (c *Cipher) HashKey(ctx context.Context, plaintext string) (string, error) {
	return c.seal.MAC(ctx, plaintext)
}

// CandidateHashes is every hash a presented API key could already be stored
// under: the selected backend's first, then — while a cutover is in progress —
// the one the previous backend would have produced.
//
// A keyed hash cannot be re-keyed: the plaintext is gone, so the re-seal sweep
// that rewrites every enveloped column cannot touch api_keys.key_hash. Looking
// a presented key up under both keys is therefore the only way a cutover does
// not invalidate every API key that exists, and it is why the previous backend
// stays bound until the keys issued under it have been rotated or expired.
func (c *Cipher) CandidateHashes(ctx context.Context, plaintext string) ([]string, error) {
	current, err := c.HashKey(ctx, plaintext)
	if err != nil {
		return nil, err
	}
	if c.previous == nil {
		return []string{current}, nil
	}
	previous, err := c.previous.MAC(ctx, plaintext)
	if err != nil {
		return nil, err
	}
	if previous == current {
		return []string{current}, nil
	}
	return []string{current, previous}, nil
}
