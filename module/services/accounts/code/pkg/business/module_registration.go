package business

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Module-registration credential issuance.
//
// A composed module federates its REST prefix with the gateway by presenting a
// signed, prefix-bound token. This service is the authority that issues it: the
// gateway already trusts this cluster's Ed25519 key, and the decision "may this
// caller own that prefix" is answered here rather than asserted by the
// registrant.
//
// The caller proves WHICH module it is with its own registration secret,
// provisioned by the composition alongside the digest declared here. A module
// holding the credential for "documents" therefore cannot obtain a token for
// "billing": the secret is compared against the entry for the prefix it asks
// for, and there is no entry a shared cluster-wide credential would satisfy.

// ErrModuleRegistrationDenied is returned for every refusal — unknown prefix,
// wrong secret, unconfigured registrar — so a caller probing the surface cannot
// tell which modules a composition declared.
var ErrModuleRegistrationDenied = errors.New("module registration denied")

// modulePrefixPattern mirrors the gateway's catalog-identity rule: one lowercase
// DNS-ish segment, no slashes, wildcards, or traversal.
var modulePrefixPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

// ModuleRegistrationMinter is the narrow signing capability issuance needs: no
// session store, no refresh rotation, no identity.
type ModuleRegistrationMinter interface {
	MintModuleRegistration(prefix string) (string, time.Time, error)
}

// moduleRegistrar holds the composition-declared policy and the signing key.
// A nil registrar denies everything: a deployment that has not said who may
// federate must not let anyone.
type moduleRegistrar struct {
	minter  ModuleRegistrationMinter
	secrets map[string][sha256.Size]byte
}

// SetModuleRegistrar wires module-registration issuance. Called once at startup
// with the composition's declared secrets; leaving it unset denies every
// registration.
func (s *Service) SetModuleRegistrar(minter ModuleRegistrationMinter, secrets map[string][sha256.Size]byte) {
	s.moduleRegistrar = &moduleRegistrar{minter: minter, secrets: secrets}
}

// ParseModuleRegistrationSecrets reads the composition-declared registration
// credentials, formatted as comma-separated `prefix:sha256hex` entries. Only the
// digest is configured here; the module holds the plaintext.
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

// ModuleMintRegistration authorizes a module against its declared registration
// secret and issues the gateway credential. Every refusal returns
// ErrModuleRegistrationDenied.
func (s *Service) ModuleMintRegistration(ctx context.Context, prefix, secret string) (string, time.Time, error) {
	registrar := s.moduleRegistrar
	if registrar == nil || registrar.minter == nil || !modulePrefixPattern.MatchString(prefix) {
		return "", time.Time{}, ErrModuleRegistrationDenied
	}

	// Comparison runs the same way whether or not the prefix was declared: an
	// undeclared prefix compares against a zero digest rather than returning
	// early, so response time does not reveal which modules a composition
	// declared. `declared` is still required, so the zero digest can never
	// authenticate on its own.
	expected, declared := registrar.secrets[prefix]
	presented := sha256.Sum256([]byte(secret))
	matched := subtle.ConstantTimeCompare(expected[:], presented[:]) == 1
	if !declared || secret == "" || !matched {
		return "", time.Time{}, ErrModuleRegistrationDenied
	}

	token, expiresAt, err := registrar.minter.MintModuleRegistration(prefix)
	if err != nil {
		return "", time.Time{}, err
	}
	// Issuing a credential that grants control over request routing is a
	// security event with no authenticated edge call behind it to carry the
	// record, so this hop emits its own.
	s.emit(ctx, "module:"+prefix, "system", EventModuleRegistrationMint, "module", prefix, "",
		map[string]any{"prefix": prefix})
	return token, expiresAt, nil
}
