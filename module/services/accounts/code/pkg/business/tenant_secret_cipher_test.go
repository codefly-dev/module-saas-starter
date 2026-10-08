package business_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// tenantAwareCipher records the organization it was asked for, so a call site
// can be held to passing the ROW's organization rather than an empty string.
type tenantAwareCipher struct {
	orgs []string
}

func (c *tenantAwareCipher) EncryptSecret(_ context.Context, purpose, plaintext string) (string, error) {
	c.orgs = append(c.orgs, "<deployment>")
	return "deployment|" + purpose + "|" + plaintext, nil
}

func (c *tenantAwareCipher) DecryptSecret(_ context.Context, purpose, envelope string) (string, error) {
	c.orgs = append(c.orgs, "<deployment>")
	return strings.TrimPrefix(envelope, "deployment|"+purpose+"|"), nil
}

func (c *tenantAwareCipher) EncryptTenantSecret(_ context.Context, orgID, purpose, plaintext string) (string, error) {
	c.orgs = append(c.orgs, orgID)
	return orgID + "|" + purpose + "|" + plaintext, nil
}

func (c *tenantAwareCipher) DecryptTenantSecret(_ context.Context, orgID, purpose, envelope string) (string, error) {
	c.orgs = append(c.orgs, orgID)
	return strings.TrimPrefix(envelope, orgID+"|"+purpose+"|"), nil
}

// A cipher that can seal per organization is asked to, and is given the
// organization rather than an empty string.
func TestTenantSecretRoutingPassesTheOrganization(t *testing.T) {
	cipher := &tenantAwareCipher{}

	sealed, err := business.SealTenantSecret(context.Background(), cipher, "org-a", "github-connector:s1", "ghp")
	require.NoError(t, err)
	require.Equal(t, "org-a|github-connector:s1|ghp", sealed)

	opened, err := business.OpenTenantSecret(context.Background(), cipher, "org-a", "github-connector:s1", sealed)
	require.NoError(t, err)
	require.Equal(t, "ghp", opened)

	require.Equal(t, []string{"org-a", "org-a"}, cipher.orgs,
		"both directions must carry the row's organization")
}

// A plain SecretCipher — what every test double implements — still works, which
// is why the routing is an assertion rather than an interface change across
// twelve fakes. A DEPLOYMENT cannot reach this path: startup refuses a cipher
// that is not a TenantSecretCipher.
func TestTenantSecretRoutingFallsBackForAPlainCipher(t *testing.T) {
	plain := plainCipher{}

	sealed, err := business.SealTenantSecret(context.Background(), plain, "org-a", "p", "v")
	require.NoError(t, err)
	require.Equal(t, "plain|p|v", sealed)

	opened, err := business.OpenTenantSecret(context.Background(), plain, "org-a", "p", sealed)
	require.NoError(t, err)
	require.Equal(t, "v", opened)
}

type plainCipher struct{}

func (plainCipher) EncryptSecret(_ context.Context, purpose, plaintext string) (string, error) {
	return "plain|" + purpose + "|" + plaintext, nil
}

func (plainCipher) DecryptSecret(_ context.Context, purpose, envelope string) (string, error) {
	return strings.TrimPrefix(envelope, "plain|"+purpose+"|"), nil
}
