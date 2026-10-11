//go:build !pure

package infra_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"
	"accounts/pkg/keyservice"
)

// The re-seal verb, end to end against a real database, with two real sealing
// backends behind the cipher.
//
// What has to be true for a cutover to be finishable: a value the outgoing
// backend sealed is rewritten under the selected one, the purpose it was bound
// to survives the move (so it still opens afterwards), re-running changes
// nothing, and the report says when nothing references the outgoing backend any
// more. That last part is what an operator acts on when withdrawing it, so a
// sweep that could not distinguish "finished" from "failed on row one" would be
// worse than none.

// reversibleSealer is a sealing backend with a tag of its own, built on an
// in-process keyed transform. It stands in for the second key service — a cell
// Vault, or a cloud KMS — in a test that must not require either: what is under
// test is the sweep, the purpose reconstruction and the report, none of which
// care which cryptography is behind the seam. The purpose IS bound (it is part
// of the sealed string and checked on open), so a sweep that rebuilt the wrong
// purpose fails here rather than passing quietly.
type reversibleSealer struct{ tag string }

func (s reversibleSealer) Tag() string      { return s.tag }
func (s reversibleSealer) Identity() string { return s.tag }

func (s reversibleSealer) Accepts(envelope keyservice.Envelope) bool {
	return envelope.Backend == s.tag
}

func (s reversibleSealer) Seal(_ context.Context, purpose, plaintext string) (string, error) {
	return purpose + "|" + plaintext, nil
}

func (s reversibleSealer) Open(_ context.Context, purpose, payload string) (string, error) {
	boundPurpose, plaintext, found := strings.Cut(payload, "|")
	if !found || boundPurpose != purpose {
		return "", business.ErrInvalidSecretEnvelope
	}
	return plaintext, nil
}

func (s reversibleSealer) MAC(_ context.Context, plaintext string) (string, error) {
	return s.tag + ":" + plaintext, nil
}

func TestResealEnvelopesMovesEveryColumnAndReportsWhatIsLeft(t *testing.T) {
	outgoing := reversibleSealer{tag: "outgoing-key-service"}
	selected := reversibleSealer{tag: "selected-key-service"}

	before, err := keyservice.NewCipher(outgoing, nil)
	require.NoError(t, err)
	cutover, err := keyservice.NewCipher(selected, outgoing)
	require.NoError(t, err)

	seeded := seedEveryEnvelopedColumn(t, before)

	outcomes, err := testStore.ResealEnvelopes(testCtx, cutover)
	require.NoError(t, err)
	require.Len(t, outcomes, 9, "every enveloped column must be reported, including the ones with no rows")

	resealed, remaining, detail := infra.ResealReport(outcomes)
	require.Empty(t, remaining,
		"nothing may still reference the outgoing backend once the sweep is done — this is the report an operator withdraws it on; detail: %v", detail)
	require.GreaterOrEqual(t, resealed, len(seeded),
		"every seeded row must have moved")

	// The value still opens, under the SAME purpose, through the cutover cipher
	// — so the sweep rebuilt the binding rather than sealing under a new one.
	for key, expected := range seeded {
		stored := expected.locate(t)
		require.True(t, strings.HasPrefix(stored, keyservice.EnvelopePrefix(selected.Tag())),
			"%s was not re-sealed under the selected backend: %q", key, stored)
		plaintext, err := cutover.DecryptSecret(testCtx, expected.purpose, stored)
		require.NoError(t, err, "%s no longer opens under the purpose it was bound to", key)
		require.Equal(t, expected.plaintext, plaintext, "%s round-tripped a different value", key)
	}

	// Restart safety: a second run is a no-op, so an interrupted cutover resumes
	// and two replicas running it cannot fight.
	outcomes, err = testStore.ResealEnvelopes(testCtx, cutover)
	require.NoError(t, err)
	again, remaining, _ := infra.ResealReport(outcomes)
	require.Zero(t, again, "re-sealing what is already sealed must change nothing")
	require.Empty(t, remaining)
}

// A row the cipher cannot open must be COUNTED, not swallowed and not fatal:
// the whole point of the report is that an operator learns the outgoing backend
// is still referenced instead of withdrawing it on a success that was partial.
func TestResealEnvelopesReportsWhatItCouldNotMove(t *testing.T) {
	selected := reversibleSealer{tag: "selected-key-service"}
	// No previous backend bound, so a value sealed by a third one cannot open.
	cipher, err := keyservice.NewCipher(selected, nil)
	require.NoError(t, err)

	orphan := keyservice.Envelope{Backend: "a-backend-nobody-bound", Payload: "whatever"}.String()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, err := storetx.Tx(ctx).Exec(ctx, `DELETE FROM webauthn_credentials`)
		return err
	}))
	userID := seedUser(t)
	deviceID := seedWebAuthnDevice(t, userID, orphan)

	outcomes, err := testStore.ResealEnvelopes(testCtx, cipher)
	require.NoError(t, err, "one unreadable row must not abort the sweep over every other column")

	_, remaining, detail := infra.ResealReport(outcomes)
	require.Equal(t, 1, remaining["a-backend-nobody-bound"],
		"the orphan must be counted so the outgoing backend is not withdrawn on a partial success; detail: %v", detail)
	require.Contains(t, strings.Join(detail, " "), "webauthn_credentials.credential_encrypted")

	// Untouched, not mangled.
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var stored string
		require.NoError(t, storetx.Tx(ctx).QueryRow(ctx,
			`SELECT credential_encrypted FROM webauthn_credentials WHERE device_id = $1`, deviceID).Scan(&stored))
		require.Equal(t, orphan, stored)
		return nil
	}))
}

// sealedRow is what one seeded column holds: the purpose its value was bound to
// and the plaintext behind it, so an assertion can prove BOTH survived the move.
type sealedRow struct {
	purpose, plaintext string
	// sealed is the stored value as seeded, so an assertion can prove a
	// protected row is untouched byte for byte.
	sealed string
	// locate reads the stored value back.
	locate func(t *testing.T) string
}

// seedEveryEnvelopedColumn puts one sealed value in each of the nine enveloped
// columns, sealed by the cipher it is given. Raw SQL under the control plane,
// because several of these tables have no business-layer constructor that would
// let a test choose the stored envelope.
func seedEveryEnvelopedColumn(t *testing.T, sealer *keyservice.Cipher) map[string]sealedRow {
	t.Helper()
	clearEnvelopedTables(t)

	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	seeded := map[string]sealedRow{}

	seal := func(purpose, plaintext string) string {
		t.Helper()
		stored, err := sealer.EncryptSecret(testCtx, purpose, plaintext)
		require.NoError(t, err)
		return stored
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			_, err := storetx.Tx(ctx).Exec(ctx, sql, args...)
			return err
		}))
	}
	record := func(table, column, purpose, plaintext, idColumn, id string) {
		seeded[table+"."+column] = sealedRow{
			purpose: purpose, plaintext: plaintext,
			locate: func(t *testing.T) string {
				t.Helper()
				var stored string
				require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
					//nolint:gosec // identifiers are literals from this test
					return storetx.Tx(ctx).QueryRow(ctx,
						"SELECT "+column+" FROM "+table+" WHERE "+idColumn+" = $1", id).Scan(&stored)
				}))
				return stored
			},
		}
	}

	deviceID := business.NewIDString()
	exec(`INSERT INTO mfa_devices (id, user_id, device_type, name, secret_encrypted)
	      VALUES ($1, $2, 'totp', 'Authenticator', $3)`,
		deviceID, userID, seal(business.MFATOTPPurpose, "JBSWY3DPEHPK3PXP"))
	record("mfa_devices", "secret_encrypted", business.MFATOTPPurpose, "JBSWY3DPEHPK3PXP", "id", deviceID)

	webAuthnDeviceID := business.NewIDString()
	exec(`INSERT INTO mfa_devices (id, user_id, device_type, name) VALUES ($1, $2, 'webauthn', 'Key')`,
		webAuthnDeviceID, userID)
	exec(`INSERT INTO webauthn_credentials (device_id, user_id, credential_id, credential_encrypted)
	      VALUES ($1, $2, $3, $4)`,
		webAuthnDeviceID, userID, []byte("credential-id"),
		seal(business.WebAuthnCredentialPurpose, `{"id":"credential"}`))
	record("webauthn_credentials", "credential_encrypted",
		business.WebAuthnCredentialPurpose, `{"id":"credential"}`, "device_id", webAuthnDeviceID)

	ceremonyID := business.NewIDString()
	exec(`INSERT INTO webauthn_ceremonies (id, token_hash, user_id, ceremony_type, session_data_encrypted, expires_at)
	      VALUES ($1, $2, $3, 'registration', $4, NOW() + INTERVAL '5 minutes')`,
		// The column constrains length(token_hash) = 64.
		ceremonyID, fmt.Sprintf("%064x", sha256.Sum256([]byte(ceremonyID))), userID,
		seal(business.WebAuthnSessionPurpose, `{"challenge":"abc"}`))
	record("webauthn_ceremonies", "session_data_encrypted",
		business.WebAuthnSessionPurpose, `{"challenge":"abc"}`, "id", ceremonyID)

	subscriptionID := business.NewIDString()
	exec(`INSERT INTO webhook_subscriptions
	        (id, org_id, url, secret_encrypted, previous_secret_encrypted, previous_secret_expires_at, active)
	      VALUES ($1, $2, 'https://example.com/hooks', $3, $4, NOW() + INTERVAL '1 day', true)`,
		subscriptionID, orgID,
		seal(business.WebhookSecretPurpose(subscriptionID), "whsec_current"),
		seal(business.WebhookSecretPurpose(subscriptionID), "whsec_previous"))
	record("webhook_subscriptions", "secret_encrypted",
		business.WebhookSecretPurpose(subscriptionID), "whsec_current", "id", subscriptionID)
	record("webhook_subscriptions", "previous_secret_encrypted",
		business.WebhookSecretPurpose(subscriptionID), "whsec_previous", "id", subscriptionID)

	sourceID := business.NewIDString()
	// boundary_node_id is a foreign key onto the org's scope tree, so the node
	// has to exist before the source can.
	nodeID := business.NewIDString()
	exec(`INSERT INTO scope_nodes (id, org_id, scope_path, kind, label)
	      VALUES ($1, $2, $3::ltree, 'collection', 'docs')`,
		nodeID, orgID, strings.ReplaceAll(nodeID, "-", "_"))
	exec(`INSERT INTO datasource_sources
	        (id, org_id, provider, repo, credential_secret_ref, webhook_secret_ref, boundary_node_id)
	      VALUES ($1, $2, 'github', 'acme/widgets', $3, $4, $5)`,
		sourceID, orgID,
		seal(business.DatasourceConnectorSecretPurpose(sourceID), "ghp_token"),
		seal(business.DatasourceWebhookSecretPurpose(sourceID), "whsec_source"),
		nodeID)
	record("datasource_sources", "credential_secret_ref",
		business.DatasourceConnectorSecretPurpose(sourceID), "ghp_token", "id", sourceID)
	record("datasource_sources", "webhook_secret_ref",
		business.DatasourceWebhookSecretPurpose(sourceID), "whsec_source", "id", sourceID)

	providerID := business.NewIDString()
	exec(`INSERT INTO org_identity_providers (id, org_id, kind, display_name, client_secret_ref)
	      VALUES ($1, $2, 'oidc', 'Example SSO', $3)`,
		providerID, orgID, seal(business.OrgIdentityProviderSecretPurpose(orgID), "idp-client-secret"))
	record("org_identity_providers", "client_secret_ref",
		business.OrgIdentityProviderSecretPurpose(orgID), "idp-client-secret", "id", providerID)

	credentialID := business.NewIDString()
	exec(`INSERT INTO connector_credentials (id, org_id, source_id, provider, secret_encrypted)
	      VALUES ($1, $2, $3, 'github', $4)`,
		credentialID, orgID, sourceID,
		seal(business.ConnectorSecretPurpose(sourceID), `{"token":"ghp"}`))
	record("connector_credentials", "secret_encrypted",
		business.ConnectorSecretPurpose(sourceID), `{"token":"ghp"}`, "id", credentialID)

	require.Len(t, seeded, 9, "every enveloped column must be seeded, or the sweep is only partly exercised")
	// Read each stored value back, so a later assertion can prove an untouched
	// row is untouched byte for byte without assuming the sealer is
	// deterministic.
	for key, row := range seeded {
		row.sealed = row.locate(t)
		seeded[key] = row
	}
	return seeded
}

func clearEnvelopedTables(t *testing.T) {
	t.Helper()
	// scope_nodes is deliberately NOT cleared: other suites in this package own
	// rows there, and installations holds a foreign key onto it.
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		for _, table := range []string{
			"connector_credentials", "datasource_sources", "org_identity_providers",
			"webhook_subscriptions", "webauthn_ceremonies", "webauthn_credentials", "mfa_devices",
		} {
			//nolint:gosec // literal table names
			if _, err := storetx.Tx(ctx).Exec(ctx, "DELETE FROM "+table); err != nil {
				return err
			}
		}
		return nil
	}))
}

func seedWebAuthnDevice(t *testing.T, userID, credentialEnvelope string) string {
	t.Helper()
	deviceID := business.NewIDString()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		if _, err := storetx.Tx(ctx).Exec(ctx,
			`INSERT INTO mfa_devices (id, user_id, device_type, name) VALUES ($1, $2, 'webauthn', 'Key')`,
			deviceID, userID); err != nil {
			return err
		}
		_, err := storetx.Tx(ctx).Exec(ctx,
			`INSERT INTO webauthn_credentials (device_id, user_id, credential_id, credential_encrypted)
			 VALUES ($1, $2, $3, $4)`, deviceID, userID, []byte("credential-"+deviceID), credentialEnvelope)
		return err
	}))
	return deviceID
}

// The crypto-shredding invariant, which is the whole point of a per-organization
// key: the sweep must LEAVE ALONE an organization whose key this deployment does
// not control.
//
// Without this the sweep would re-seal a customer-held organization's
// credentials under a key we hold — undoing the single property that key exists
// to provide, and reporting a successful migration while doing it. For a revoked
// key it would attempt a decrypt that cannot succeed, which the datasource
// failure path turns into telling the customer to reconnect a source whose data
// they instructed us to destroy.
func TestResealEnvelopesLeavesAloneAnOrganizationWhoseKeyWeDoNotControl(t *testing.T) {
	for name, bind := range map[string]string{
		"the organization holds its own key": `INSERT INTO org_key_bindings (org_id, key_ref, customer_held)
		                                      VALUES ($1, 'customer-held-key', true)`,
		"the organization revoked its key": `INSERT INTO org_key_bindings (org_id, key_ref, customer_held, revoked_at, revoked_reason)
		                                     VALUES ($1, 'destroyed-key', false, NOW(), 'customer instruction')`,
	} {
		t.Run(name, func(t *testing.T) {
			outgoing := reversibleSealer{tag: "outgoing-key-service"}
			selected := reversibleSealer{tag: "selected-key-service"}
			before, err := keyservice.NewCipher(outgoing, nil)
			require.NoError(t, err)
			cutover, err := keyservice.NewCipher(selected, outgoing)
			require.NoError(t, err)

			seeded := seedEveryEnvelopedColumn(t, before)

			// Bind every organization the fixture created. The fixture seeds one
			// org, so this covers each organization-scoped column.
			require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
				rows, err := storetx.Tx(ctx).Query(ctx, `SELECT DISTINCT org_id::text FROM webhook_subscriptions`)
				require.NoError(t, err)
				var orgs []string
				for rows.Next() {
					var orgID string
					require.NoError(t, rows.Scan(&orgID))
					orgs = append(orgs, orgID)
				}
				rows.Close()
				require.NotEmpty(t, orgs)
				for _, orgID := range orgs {
					if _, err := storetx.Tx(ctx).Exec(ctx, bind, orgID); err != nil {
						return err
					}
				}
				return nil
			}))

			outcomes, err := testStore.ResealEnvelopes(testCtx, cutover)
			require.NoError(t, err)

			protected := 0
			for _, outcome := range outcomes {
				protected += outcome.Protected
			}
			require.Positive(t, protected,
				"the organization's columns must be reported as left alone, not silently skipped")

			// Every organization-scoped value is untouched: still sealed by the
			// outgoing backend, byte for byte.
			for key, expected := range seeded {
				stored := expected.locate(t)
				if orgScopedEnvelopedColumn(key) {
					require.Equal(t, expected.sealed, stored,
						"%s belongs to an organization whose key we do not control and must be untouched", key)
					continue
				}
				// A user's MFA seed and WebAuthn credential are not any one
				// organization's, so they move as usual.
				require.True(t, strings.HasPrefix(stored, keyservice.EnvelopePrefix(selected.Tag())),
					"%s is not organization-scoped and must still be re-sealed", key)
			}

			// And the report must not count them as outstanding work: an
			// operator chasing Remaining to zero would never finish, and would
			// eventually force it.
			_, remaining, detail := infra.ResealReport(outcomes)
			require.Empty(t, remaining,
				"a protected organization is never 'still to do'; detail: %v", detail)
			require.Contains(t, strings.Join(detail, " "), "left alone")
		})
	}
}

// orgScopedEnvelopedColumn reports whether a seeded column belongs to an
// organization. Kept beside the assertion rather than derived, so adding an
// organization-scoped column without deciding this fails the test above.
func orgScopedEnvelopedColumn(tableAndColumn string) bool {
	switch tableAndColumn {
	case "webhook_subscriptions.secret_encrypted",
		"webhook_subscriptions.previous_secret_encrypted",
		"datasource_sources.credential_secret_ref",
		"datasource_sources.webhook_secret_ref",
		"org_identity_providers.client_secret_ref",
		"connector_credentials.secret_encrypted":
		return true
	default:
		return false
	}
}
