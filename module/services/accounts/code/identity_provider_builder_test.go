package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// fakeSecretCipher is an in-memory SecretCipher for builder tests. It records
// the purpose/envelope it was asked to decrypt so a test can assert the secret
// is fetched with the org-bound purpose, never reused across orgs.
type fakeSecretCipher struct {
	plaintext    string
	decryptErr   error
	lastPurpose  string
	lastEnvelope string
}

func (f *fakeSecretCipher) EncryptSecret(_ context.Context, purpose, _ string) (string, error) {
	return "cfs1:vault-transit:" + purpose, nil
}

func (f *fakeSecretCipher) DecryptSecret(_ context.Context, purpose, envelope string) (string, error) {
	f.lastPurpose = purpose
	f.lastEnvelope = envelope
	if f.decryptErr != nil {
		return "", f.decryptErr
	}
	return f.plaintext, nil
}

const builderOrgID = "11111111-1111-1111-1111-111111111111"

// pinnedOIDCProvider has both JWKS and token endpoints set, so Build never
// touches the network (discovery only runs when an endpoint is missing).
func pinnedOIDCProvider() *business.OrgIdentityProvider {
	return &business.OrgIdentityProvider{
		OrgID:           builderOrgID,
		Kind:            business.IdentityProviderKindOIDC,
		Issuer:          "https://idp.example/",
		ClientID:        "client-abc",
		ClientSecretRef: "cfs1:vault-transit:opaque",
		JWKSURL:         "https://idp.example/jwks",
		TokenURL:        "https://idp.example/token",
		Audience:        "api://acme",
	}
}

func TestBuilderBuildsPinnedOIDCStackWithoutNetwork(t *testing.T) {
	cipher := &fakeSecretCipher{plaintext: "s3cret"}
	builder := oidcProviderStackBuilder{cipher: cipher}

	stack, err := builder.Build(context.Background(), pinnedOIDCProvider())
	require.NoError(t, err)
	require.Equal(t, "oidc:"+builderOrgID, stack.Name)
	require.NotNil(t, stack.Validator)
	require.NotNil(t, stack.Exchanger)
	// The secret is decrypted with the org-bound purpose against the stored
	// envelope — the two properties that keep tenants' secrets from crossing.
	require.Equal(t, business.OrgIdentityProviderSecretPurpose(builderOrgID), cipher.lastPurpose)
	require.Equal(t, "cfs1:vault-transit:opaque", cipher.lastEnvelope)
}

func TestBuilderRejectsHeaderJWTKind(t *testing.T) {
	p := pinnedOIDCProvider()
	p.Kind = business.IdentityProviderKindHeaderJWT

	_, err := oidcProviderStackBuilder{cipher: &fakeSecretCipher{plaintext: "s3cret"}}.
		Build(context.Background(), p)
	require.ErrorIs(t, err, business.ErrProviderKindUnsupported,
		"header-jwt config is storable but has no runtime stack yet — build must refuse, not stub")
}

func TestBuilderRequiresIssuerClientAndSecret(t *testing.T) {
	cipher := &fakeSecretCipher{plaintext: "s3cret"}
	cases := map[string]func(*business.OrgIdentityProvider){
		"missing issuer":     func(p *business.OrgIdentityProvider) { p.Issuer = "" },
		"missing client id":  func(p *business.OrgIdentityProvider) { p.ClientID = "" },
		"missing secret ref": func(p *business.OrgIdentityProvider) { p.ClientSecretRef = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := pinnedOIDCProvider()
			mutate(p)
			_, err := oidcProviderStackBuilder{cipher: cipher}.Build(context.Background(), p)
			require.Error(t, err)
		})
	}
}

func TestBuilderPropagatesDecryptError(t *testing.T) {
	sentinel := errors.New("vault unavailable")
	_, err := oidcProviderStackBuilder{cipher: &fakeSecretCipher{decryptErr: sentinel}}.
		Build(context.Background(), pinnedOIDCProvider())
	require.ErrorIs(t, err, sentinel, "a decrypt failure must fail the build, never fall back to plaintext")
}
