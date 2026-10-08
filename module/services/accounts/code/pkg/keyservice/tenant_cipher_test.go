package keyservice

// The per-organization key. What has to hold:
//
//   - an organization with no key of its own is sealed under the deployment's,
//     and that stays the default;
//   - an organization with one is sealed under it, and a value sealed under the
//     deployment's key BEFORE the binding existed still opens;
//   - a revoked key makes its credentials unreadable, reported as that and not
//     as a credential the user should re-enter;
//   - an absent organization is REFUSED, never quietly sealed under the
//     deployment's key — that would silently betray the one customer who asked
//     for their own.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// bindings is an in-memory OrgKeyBindings.
type bindings struct {
	rows  map[string]*OrgKeyBinding
	calls int
	err   error
}

func (b *bindings) OrgKeyBinding(_ context.Context, orgID string) (*OrgKeyBinding, error) {
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	return b.rows[orgID], nil
}

// keyFactory builds a distinct sealer per key reference, counting how often it
// is asked so the cache can be asserted.
type keyFactory struct {
	built int
	err   error
}

func (f *keyFactory) SealerFor(_ context.Context, keyRef string) (Sealer, error) {
	f.built++
	if f.err != nil {
		return nil, f.err
	}
	return testSealer{tag: "test-key-service", key: keyRef}, nil
}

// testSealer is a sealer whose stored payload names its key, the property every
// real backend has and the one routing depends on.
type testSealer struct{ tag, key string }

func (s testSealer) Tag() string      { return s.tag }
func (s testSealer) Identity() string { return s.tag + ":" + s.key }

func (s testSealer) Accepts(envelope Envelope) bool {
	if envelope.Backend != s.tag {
		return false
	}
	key, _, found := strings.Cut(envelope.Payload, "|")
	return found && key == s.key
}

func (s testSealer) Seal(_ context.Context, purpose, plaintext string) (string, error) {
	return s.key + "|" + purpose + "|" + plaintext, nil
}

func (s testSealer) Open(_ context.Context, purpose, payload string) (string, error) {
	parts := strings.SplitN(payload, "|", 3)
	if len(parts) != 3 || parts[0] != s.key || parts[1] != purpose {
		return "", business.ErrInvalidSecretEnvelope
	}
	return parts[2], nil
}

func (s testSealer) MAC(_ context.Context, plaintext string) (string, error) {
	return s.key + ":" + plaintext, nil
}

func newTenantCipher(t *testing.T, rows map[string]*OrgKeyBinding) (*TenantCipher, *bindings, *keyFactory) {
	t.Helper()
	deployment, err := NewCipher(testSealer{tag: "test-key-service", key: "deployment"}, nil)
	require.NoError(t, err)
	source := &bindings{rows: rows}
	factory := &keyFactory{}
	cipher, err := NewTenantCipher(deployment, factory, source)
	require.NoError(t, err)
	return cipher, source, factory
}

func TestTenantCipherSealsUnderTheDeploymentKeyWhenAnOrganizationHasNone(t *testing.T) {
	cipher, _, factory := newTenantCipher(t, nil)

	stored, err := cipher.EncryptTenantSecret(t.Context(), "org-a", "github-connector:s1", "ghp_token")
	require.NoError(t, err)
	require.Contains(t, stored, "deployment|", "no binding means the deployment's key, which is the default")
	require.Zero(t, factory.built, "no organization key was needed, so none may be built")

	opened, err := cipher.DecryptTenantSecret(t.Context(), "org-a", "github-connector:s1", stored)
	require.NoError(t, err)
	require.Equal(t, "ghp_token", opened)
}

func TestTenantCipherSealsUnderTheOrganizationsOwnKey(t *testing.T) {
	cipher, _, _ := newTenantCipher(t, map[string]*OrgKeyBinding{
		"org-a": {KeyRef: "acme-key", CustomerHeld: true},
	})

	stored, err := cipher.EncryptTenantSecret(t.Context(), "org-a", "org-idp:org-a", "idp-secret")
	require.NoError(t, err)
	require.Contains(t, stored, "acme-key|")
	require.NotContains(t, stored, "deployment|")

	opened, err := cipher.DecryptTenantSecret(t.Context(), "org-a", "org-idp:org-a", stored)
	require.NoError(t, err)
	require.Equal(t, "idp-secret", opened)

	// Another organization's key must not open it, even with the same purpose.
	cipherB, _, _ := newTenantCipher(t, map[string]*OrgKeyBinding{
		"org-b": {KeyRef: "other-key"},
	})
	_, err = cipherB.DecryptTenantSecret(t.Context(), "org-b", "org-idp:org-a", stored)
	require.Error(t, err)
}

// The cutover case: a value sealed under the deployment's key before the
// organization was given one must keep opening, or granting a customer a key
// would make their existing credentials unreadable.
func TestTenantCipherStillOpensValuesSealedBeforeTheBindingExisted(t *testing.T) {
	cipher, source, _ := newTenantCipher(t, nil)
	before, err := cipher.EncryptTenantSecret(t.Context(), "org-a", "webhook-signing:w1", "whsec_old")
	require.NoError(t, err)

	// The platform now gives the organization its own key.
	source.rows = map[string]*OrgKeyBinding{"org-a": {KeyRef: "acme-key"}}

	opened, err := cipher.DecryptTenantSecret(t.Context(), "org-a", "webhook-signing:w1", before)
	require.NoError(t, err, "a value predating the binding must still open")
	require.Equal(t, "whsec_old", opened)

	// New values go under the organization's key.
	after, err := cipher.EncryptTenantSecret(t.Context(), "org-a", "webhook-signing:w1", "whsec_new")
	require.NoError(t, err)
	require.Contains(t, after, "acme-key|")

	// And the sweep moves the old one, idempotently.
	resealed, changed, err := cipher.ResealTenantSecret(t.Context(), "org-a", "webhook-signing:w1", before)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, resealed, "acme-key|")
	_, changed, err = cipher.ResealTenantSecret(t.Context(), "org-a", "webhook-signing:w1", resealed)
	require.NoError(t, err)
	require.False(t, changed, "re-sealing what is already under the key must change nothing")
}

// Crypto-shredding: the credentials are unreadable, and the reason says so
// rather than reading as a credential the user should re-enter.
func TestTenantCipherRefusesARevokedKeyWithoutCondemningTheCredential(t *testing.T) {
	cipher, source, _ := newTenantCipher(t, map[string]*OrgKeyBinding{
		"org-a": {KeyRef: "acme-key", CustomerHeld: true},
	})
	stored, err := cipher.EncryptTenantSecret(t.Context(), "org-a", "org-idp:org-a", "idp-secret")
	require.NoError(t, err)

	source.rows["org-a"] = &OrgKeyBinding{
		KeyRef: "acme-key", CustomerHeld: true, RevokedReason: "customer instruction",
	}

	_, err = cipher.DecryptTenantSecret(t.Context(), "org-a", "org-idp:org-a", stored)
	require.ErrorIs(t, err, ErrKeyRevoked)
	require.NotErrorIs(t, err, business.ErrInvalidSecretEnvelope,
		"a destroyed key is not a credential to re-enter — telling the customer to reconnect would be the opposite of what they asked for")
	require.Contains(t, err.Error(), "customer instruction")

	// And nothing further may be sealed under it.
	_, err = cipher.EncryptTenantSecret(t.Context(), "org-a", "org-idp:org-a", "new-secret")
	require.ErrorIs(t, err, ErrKeyRevoked)
}

// The property the explicit argument exists for.
func TestTenantCipherRefusesAnAbsentOrganization(t *testing.T) {
	cipher, _, _ := newTenantCipher(t, map[string]*OrgKeyBinding{
		"org-a": {KeyRef: "acme-key"},
	})

	_, err := cipher.EncryptTenantSecret(t.Context(), "", "org-idp:org-a", "secret")
	require.Error(t, err, "an absent organization must never fall back to the deployment's key")
	require.Contains(t, err.Error(), "requires the organization")

	_, err = cipher.DecryptTenantSecret(t.Context(), "", "org-idp:org-a", "cfs1:test-key-service:acme-key|p|v")
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires the organization")
}

// A sealer is built once per key reference, not per request: building one is a
// round trip to the key service.
func TestTenantCipherCachesOneSealerPerKey(t *testing.T) {
	cipher, _, factory := newTenantCipher(t, map[string]*OrgKeyBinding{
		"org-a": {KeyRef: "shared-key"},
		"org-b": {KeyRef: "shared-key"},
	})
	for range 5 {
		_, err := cipher.EncryptTenantSecret(t.Context(), "org-a", "p", "v")
		require.NoError(t, err)
		_, err = cipher.EncryptTenantSecret(t.Context(), "org-b", "p", "v")
		require.NoError(t, err)
	}
	require.Equal(t, 1, factory.built, "two organizations on one key share one sealer")
}

// A binding that cannot be read must refuse, not fall through to the
// deployment's key: "the lookup failed" and "there is no binding" are different
// answers, and conflating them seals a customer's data under our key.
func TestTenantCipherRefusesWhenTheBindingCannotBeRead(t *testing.T) {
	cipher, source, _ := newTenantCipher(t, nil)
	source.err = errors.New("database unavailable")

	_, err := cipher.EncryptTenantSecret(t.Context(), "org-a", "p", "v")
	require.Error(t, err)
	require.Contains(t, err.Error(), "database unavailable")

	_, err = cipher.DecryptTenantSecret(t.Context(), "org-a", "p", "cfs1:test-key-service:deployment|p|v")
	require.Error(t, err)
	require.Contains(t, err.Error(), "database unavailable")
}

// The keyed hash never moves off the deployment's key, whatever the organization
// has: API-key lookup is BY the hash, and the lookup is what discovers the
// organization.
func TestTenantCipherKeyedHashIsAlwaysTheDeploymentsOwn(t *testing.T) {
	cipher, _, _ := newTenantCipher(t, map[string]*OrgKeyBinding{
		"org-a": {KeyRef: "acme-key"},
	})
	hash, err := cipher.HashKey(t.Context(), "cfly_sk_live_example")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(hash, "deployment:"),
		"the hash must be the deployment's, got %q", hash)
}

// The row's organization and the transaction's scope must agree. Row-level
// security binds the SCOPE, never the argument, so a disagreement means one of
// them is wrong — and guessing which seals a customer's credential under another
// customer's key. Nothing else in the stack catches this.
func TestTenantCipherRefusesARowWhoseOrganizationDisagreesWithTheScope(t *testing.T) {
	cipher, _, _ := newTenantCipher(t, map[string]*OrgKeyBinding{
		"org-a": {KeyRef: "acme-key"},
		"org-b": {KeyRef: "other-key"},
	})

	scoped := func(org string) context.Context {
		return context.WithValue(t.Context(), scopeKeyForTest{}, org)
	}
	previous := VerifiedScope
	VerifiedScope = func(ctx context.Context) (string, bool) {
		org, ok := ctx.Value(scopeKeyForTest{}).(string)
		return org, ok
	}
	t.Cleanup(func() { VerifiedScope = previous })

	// Agreeing is fine.
	_, err := cipher.EncryptTenantSecret(scoped("org-a"), "org-a", "org-idp:org-a", "secret")
	require.NoError(t, err)

	// Disagreeing is refused, both directions, on both verbs.
	_, err = cipher.EncryptTenantSecret(scoped("org-b"), "org-a", "org-idp:org-a", "secret")
	require.Error(t, err)
	require.Contains(t, err.Error(), "the row and the scope disagree")

	stored, err := cipher.EncryptTenantSecret(scoped("org-a"), "org-a", "org-idp:org-a", "secret")
	require.NoError(t, err)
	_, err = cipher.DecryptTenantSecret(scoped("org-b"), "org-a", "org-idp:org-a", stored)
	require.Error(t, err)
	require.Contains(t, err.Error(), "the row and the scope disagree")

	// A path with NO request scope is the delivery worker, and is legitimate:
	// the outbound sender opens a subscription's secret as app_webhook_worker,
	// outside any tenant transaction, with only the row to go on.
	_, err = cipher.DecryptTenantSecret(t.Context(), "org-a", "org-idp:org-a", stored)
	require.NoError(t, err, "a delivery worker has no request scope and must still open the row's secret")
}

type scopeKeyForTest struct{}
