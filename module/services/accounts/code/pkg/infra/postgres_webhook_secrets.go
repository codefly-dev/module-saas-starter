package infra

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

const vaultSecretEnvelopePrefix = "cfs1:vault-transit:"

// MigrateLegacyWebhookSecrets upgrades pre-migration plaintext keys before the
// server accepts requests. Empty legacy keys came from the old broken create
// path; they receive an encrypted random replacement and are disabled because
// no consumer could have learned that replacement.
func (s *PostgresStore) MigrateLegacyWebhookSecrets(ctx context.Context, cipher business.SecretCipher) (migrated, disabled int, err error) {
	if cipher == nil {
		return 0, 0, errors.New("webhook secret cipher is required")
	}
	type legacySecret struct {
		id, orgID, plaintext string
		active               bool
	}
	var legacy []legacySecret
	if err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		rows, err := s.getQueryExecutor(ctx).Query(ctx, `
			SELECT id::text, org_id::text, secret_encrypted, active
			FROM webhook_subscriptions
			WHERE secret_encrypted NOT LIKE $1`, vaultSecretEnvelopePrefix+"%")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item legacySecret
			if err := rows.Scan(&item.id, &item.orgID, &item.plaintext, &item.active); err != nil {
				return err
			}
			legacy = append(legacy, item)
		}
		return rows.Err()
	}); err != nil {
		return 0, 0, err
	}

	for _, item := range legacy {
		plaintext := item.plaintext
		shouldDisable := strings.TrimSpace(plaintext) == ""
		if shouldDisable {
			raw := make([]byte, 32)
			if _, err := rand.Read(raw); err != nil {
				return migrated, disabled, fmt.Errorf("generate replacement webhook secret %s: %w", item.id, err)
			}
			plaintext = "whsec_" + base64.RawURLEncoding.EncodeToString(raw)
		}
		envelope, err := cipher.EncryptSecret(ctx, business.WebhookSecretPurpose(item.id), plaintext)
		if err != nil {
			return migrated, disabled, fmt.Errorf("encrypt legacy webhook secret %s: %w", item.id, err)
		}
		if err := s.WithOrgTx(ctx, item.orgID, func(ctx context.Context) error {
			var updated bool
			err := s.getQueryExecutor(ctx).QueryRow(ctx, `
				UPDATE webhook_subscriptions
				SET secret_encrypted = $2,
				    active = CASE WHEN $4 THEN false ELSE active END,
				    updated_at = NOW()
				WHERE id = $1
				  AND secret_encrypted = $3
				RETURNING true`, item.id, envelope, item.plaintext, shouldDisable).Scan(&updated)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err == nil && updated {
				migrated++
				if shouldDisable && item.active {
					disabled++
				}
			}
			return err
		}); err != nil {
			return migrated, disabled, err
		}
	}
	return migrated, disabled, nil
}
