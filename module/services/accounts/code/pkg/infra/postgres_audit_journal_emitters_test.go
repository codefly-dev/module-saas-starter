//go:build !pure

package infra_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/infra/storetx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// warehouseStorage is the AuditStorage of a warehouse the test cannot reach. It
// keeps what it is asked to append, so the test can show the entries went to the
// journal and nowhere else.
type warehouseStorage struct{ appended []business.AuditEntry }

func (w *warehouseStorage) Append(_ context.Context, entry business.AuditEntry) error {
	w.appended = append(w.appended, entry)
	return nil
}

func (w *warehouseStorage) List(context.Context, business.AuditRead) ([]business.AuditEntry, string, error) {
	return nil, "", errors.New("not read in this test")
}

func (w *warehouseStorage) Aggregate(context.Context, business.AuditRead, business.AuditAggregationSpec) ([]business.AuditAggregateBucket, error) {
	return nil, errors.New("not read in this test")
}

// passkeyEngine answers the WebAuthn ceremonies with fixed proofs: the test is
// about what the registration writes, not about the protocol.
type passkeyEngine struct{}

func (passkeyEngine) BeginRegistration(context.Context, business.WebAuthnUser) ([]byte, []byte, time.Time, error) {
	return []byte(`{"challenge":"registration"}`), []byte(`{"state":"registration"}`), time.Now().Add(time.Minute), nil
}

func (passkeyEngine) FinishRegistration(_ context.Context, _ business.WebAuthnUser, session, response []byte) (*business.WebAuthnCredentialResult, error) {
	if string(session) != `{"state":"registration"}` || string(response) != `{"credential":"registration"}` {
		return nil, errors.New("invalid registration proof")
	}
	return &business.WebAuthnCredentialResult{ID: []byte("credential-" + uuid.NewString()), CredentialJSON: []byte(`{"counter":0}`)}, nil
}

func (passkeyEngine) BeginLogin(context.Context, business.WebAuthnUser) ([]byte, []byte, time.Time, error) {
	return nil, nil, time.Time{}, errors.New("not used in this test")
}

func (passkeyEngine) FinishLogin(context.Context, business.WebAuthnUser, []byte, []byte) (*business.WebAuthnCredentialResult, error) {
	return nil, errors.New("not used in this test")
}

// Under the warehouse backend every audit entry is the journal's, and the ones
// with no organization are written inside a user-scoped transaction, where
// publish_domain_event used to refuse them. The WebAuthn registration and the MFA
// enrolment write theirs there, so this drives the real operations against the
// real function: each entry must commit with its mutation as a journal row for no
// tenant, in the user's name, and reach no storage.
func TestWarehouseBackendJournalsTheUserScopedEntriesWithNoOrganization(t *testing.T) {
	warehouse := &warehouseStorage{}
	audit, err := business.NewAuditService(business.AuditServiceConfig{
		Backend:   business.AuditBackendWarehouse,
		Store:     testStore,
		Producer:  testStore,
		Transport: auditJournalTransport(t),
		Warehouse: warehouse,
	})
	require.NoError(t, err)
	service, err := business.NewService(testStore)
	require.NoError(t, err)
	service.SetAuditService(audit)
	service.SetMFASecretCipher(webhookProjectionCipher{})
	service.SetWebAuthnEngine(passkeyEngine{})
	require.NoError(t, service.VerifyAuditWiring())

	user := seedUser(t)

	// WebAuthn registration: webauthn.go begins and finishes it in one
	// user-scoped transaction each, and each writes an entry.
	ceremony, _, err := service.BeginWebAuthnRegistration(testCtx, user)
	require.NoError(t, err)
	passkey, err := service.FinishWebAuthnRegistration(testCtx, user, ceremony, `{"credential":"registration"}`, "Passkey")
	require.NoError(t, err)

	// MFA: mfa.go enrols, then revokes, a TOTP device and rotates the backup
	// codes, each in a user-scoped transaction of its own.
	_, _, err = service.SetupTOTP(testCtx, user)
	require.NoError(t, err)
	devices, err := service.ListMFADevices(testCtx, user)
	require.NoError(t, err)
	var totp string
	for _, device := range devices {
		if device.DeviceType == "totp" {
			totp = device.ID
		}
	}
	require.NotEmpty(t, totp, "the TOTP device the enrolment created")
	require.NoError(t, service.RevokeMFADevice(testCtx, user, totp))
	_, err = service.GenerateBackupCodes(testCtx, user)
	require.NoError(t, err)

	want := []business.EventType{
		business.EventMFAWebAuthnRegStarted,
		business.EventMFAWebAuthnRegistered,
		business.EventMFATOTPSetupStarted,
		business.EventMFADeviceRevoked,
		business.EventMFABackupGenerated,
	}
	var got []business.AuditEntry
	var tenanted, auditRows int
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := storetx.Tx(ctx)
		rows, err := tx.Query(ctx, `SELECT data FROM domain_events
			WHERE type = $1 AND actor_principal_id = $2 AND tenant_id IS NULL
			ORDER BY seq`, business.AuditRecordedEventType, user)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			entry, err := business.AuditEntryFromJournal(data)
			if err != nil {
				return err
			}
			got = append(got, entry)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM domain_events WHERE actor_principal_id = $1 AND tenant_id IS NOT NULL`, user).Scan(&tenanted); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE actor_id = $1::uuid`, user).Scan(&auditRows)
	}))

	var types []business.EventType
	for _, entry := range got {
		types = append(types, entry.EventType)
		require.Equal(t, user, entry.ActorID)
		require.Equal(t, business.ActorTypeUser, entry.ActorType)
		require.Empty(t, entry.OrgID)
	}
	require.Equal(t, want, types, "one journal row per entry, in the order the operations wrote them")
	require.Equal(t, passkey.ID, got[1].ResourceID, "the registration entry names the device it registered")
	require.Zero(t, tenanted, "no entry of this user was given a tenant")
	require.Zero(t, auditRows, "audit_events keeps no history under the warehouse backend")
	require.Empty(t, warehouse.appended, "the journal is the entry's only copy until a delivery appends it")
}
