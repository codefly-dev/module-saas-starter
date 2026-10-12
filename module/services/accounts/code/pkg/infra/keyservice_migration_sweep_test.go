//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/infra/storetx"
	"accounts/pkg/keyservice"
)

// The legacy sweeps upgrade PRE-ENVELOPE plaintext. They must not touch a value
// another key-service backend already sealed.
//
// The predicate used to be "not sealed by Vault", which is not the same question:
// a value sealed by any other backend — or under a later framing, like the
// per-purpose Transit split — matches it, and the sweep then re-seals the
// ENVELOPE STRING as though it were the secret. The row ends up doubly wrapped,
// unreadable forever, counted as a successful migration, and wrapped once more on
// every restart. For a webhook secret that is unrecoverable: the consumer shares
// it, so the host cannot regenerate it.
//
// Each sweep is asserted on its own fixture. The MFA sweep runs first in
// work.go, so a fixture carrying both would fail there and never exercise the
// webhook sweep — a test that passed for the wrong reason and would go green
// while webhook secrets were still being destroyed.

// foreignBackendCipher seals under a backend this deployment does not bind, and
// refuses to be used as one: the sweep must skip its values without ever calling
// it, so any call is the failure.
type foreignBackendCipher struct{ t *testing.T }

func (c foreignBackendCipher) EncryptSecret(_ context.Context, purpose, plaintext string) (string, error) {
	c.t.Fatalf("the sweep re-sealed a value another backend had already sealed (purpose %q): %q", purpose, plaintext)
	return "", nil
}

func (c foreignBackendCipher) DecryptSecret(_ context.Context, _, envelope string) (string, error) {
	return envelope, nil
}

func foreignEnvelopes() map[string]string {
	return map[string]string{
		"cloud kms": keyservice.Envelope{
			Backend: keyservice.TagGCPKMS,
			Payload: "1:" + "Zmluz2VyAAAAAAAA" + ":Y2lwaGVydGV4dA",
		}.String(),
		// A framing this build cannot open is still an envelope, and sweeping it
		// is destruction rather than a refusal.
		"a later framing": "cfs2:vault-transit:mfa-totp:Y2lwaGVydGV4dA",
	}
}

func TestMigrateLegacyMFASecretsLeavesAnotherBackendsEnvelopeAlone(t *testing.T) {
	for name, sealed := range foreignEnvelopes() {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
				_, err := storetx.Tx(ctx).Exec(ctx, `DELETE FROM mfa_devices`)
				return err
			}))
			userID := seedUser(t)
			device := &business.MFADevice{
				ID: business.NewIDString(), UserID: userID, DeviceType: "totp",
				Name: "Sealed elsewhere", SecretEncrypted: sealed,
			}
			require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
				return testStore.CreateMFADevice(ctx, device)
			}))

			migrated, err := testStore.MigrateLegacyMFASecrets(testCtx, foreignBackendCipher{t})
			require.NoError(t, err,
				"a foreign envelope must not be read as a legacy base32 seed, which refuses boot")
			require.Zero(t, migrated)

			require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
				got, err := testStore.GetMFADevice(ctx, device.ID)
				require.NoError(t, err)
				require.Equal(t, sealed, got.SecretEncrypted, "the stored envelope must be untouched")
				return nil
			}))
		})
	}
}

func TestMigrateLegacyWebhookSecretsLeavesAnotherBackendsEnvelopeAlone(t *testing.T) {
	for name, sealed := range foreignEnvelopes() {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
				_, err := storetx.Tx(ctx).Exec(ctx, `DELETE FROM webhook_subscriptions`)
				return err
			}))
			ownerID := seedUser(t)
			orgID := seedOrg(t, ownerID)
			subscription := &business.WebhookSubscription{
				ID: business.NewIDString(), OrgID: orgID,
				URL: "https://example.com/hooks", Events: []string{"saas.user.registered"},
				SecretEncrypted: sealed, Active: true,
			}
			require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
				return testStore.CreateWebhookSubscription(ctx, subscription)
			}))

			migrated, disabled, err := testStore.MigrateLegacyWebhookSecrets(testCtx, foreignBackendCipher{t})
			require.NoError(t, err)
			require.Zero(t, migrated)
			require.Zero(t, disabled)

			require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
				got, err := testStore.GetWebhookSubscription(ctx, subscription.ID)
				require.NoError(t, err)
				require.Equal(t, sealed, got.SecretEncrypted,
					"the stored envelope must be untouched, not re-sealed as its own plaintext")
				require.True(t, got.Active, "a sealed secret is not an empty one, so the endpoint stays active")
				return nil
			}))
		})
	}
}
