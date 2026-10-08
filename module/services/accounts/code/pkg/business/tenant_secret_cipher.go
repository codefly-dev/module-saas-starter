package business

import "context"

// TenantSecretCipher is a SecretCipher that can seal under the organization's
// own key when it has one.
//
// It is an extension rather than a replacement because the question it adds is
// not one every sealed value has: a person's MFA seed and WebAuthn credential
// belong to someone who may be in many organizations, so "whose key" has no
// answer for them and they stay on the deployment's.
type TenantSecretCipher interface {
	SecretCipher
	EncryptTenantSecret(ctx context.Context, orgID, purpose, plaintext string) (string, error)
	DecryptTenantSecret(ctx context.Context, orgID, purpose, envelope string) (string, error)
}

// SealTenantSecret and OpenTenantSecret route an organization-scoped value
// through the per-organization key where the wiring provides one.
//
// The organization comes from the ROW being sealed — source.OrgID, sub.OrgID,
// the organization a provider is configured for — never from the request
// context: the delivery paths have no request identity at all, and a value's
// organization is a property of the row rather than of whoever is asking.
//
// The fallback exists for test doubles, which implement only SecretCipher.
// A DEPLOYMENT cannot reach it: startup refuses a cipher that is not a
// TenantSecretCipher (requireTenantCipher, work.go), so "BYOK quietly did not
// happen" is a boot failure rather than something to discover from a row sealed
// under the wrong key.
func SealTenantSecret(
	ctx context.Context,
	cipher SecretCipher,
	orgID, purpose, plaintext string,
) (string, error) {
	if tenant, ok := cipher.(TenantSecretCipher); ok {
		return tenant.EncryptTenantSecret(ctx, orgID, purpose, plaintext)
	}
	return cipher.EncryptSecret(ctx, purpose, plaintext)
}

func OpenTenantSecret(
	ctx context.Context,
	cipher SecretCipher,
	orgID, purpose, envelope string,
) (string, error) {
	if tenant, ok := cipher.(TenantSecretCipher); ok {
		return tenant.DecryptTenantSecret(ctx, orgID, purpose, envelope)
	}
	return cipher.DecryptSecret(ctx, purpose, envelope)
}
