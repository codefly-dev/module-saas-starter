//go:build !pure

package infra_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"accounts/pkg/business"
	eventsv1 "accounts/pkg/gen/saas/events/v1"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// These tests hold publish_domain_event's tenant guard to what migration 36
// leaves of it, against the real function and the real roles: the journal record
// of an audit entry with no organization is admitted from a user-scoped
// transaction for that user's own action, and nothing else tenant-less is.

func auditJournalTransport(t *testing.T) *infra.PostgresEventTransport {
	t.Helper()
	return infra.NewPostgresEventTransport(infra.NewPostgresJobStore(testPool), testPool,
		"audit-journal-"+uuid.NewString(), time.Minute)
}

// auditJournalEnvelopeOf is the envelope the emitter publishes for an entry, so
// the tests exercise what is actually sent and not a stand-in for it.
func auditJournalEnvelopeOf(t *testing.T, actorID, orgID string) *eventsv1.EventEnvelope {
	t.Helper()
	envelope, err := business.NewAuditJournalEnvelope(business.AuditEntry{
		ID:        uuid.NewString(),
		EventType: business.EventMFATOTPSetupStarted,
		ActorID:   actorID, ActorType: business.ActorTypeUser,
		Resource: "mfa_device", ResourceID: uuid.NewString(),
		OrgID:     orgID,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	return envelope
}

func publishInUserTx(transport *infra.PostgresEventTransport, userID string, envelope *eventsv1.EventEnvelope) error {
	return testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		return transport.Publish(ctx, storetx.Tx(ctx), envelope)
	})
}

func requireTenantGuardRefusal(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "want a database refusal, got %v", err)
	require.Equal(t, "42501", pgErr.Code, "insufficient_privilege")
	require.Contains(t, pgErr.Message, "event tenant does not match the signed request scope")
}

type journalRow struct {
	Type, Actor string
	TenantNull  bool
}

// journalRowsOf reads the committed journal rows under the control plane, which
// sees every tenant's and the tenant-less ones.
func journalRowsOf(t *testing.T, id string) []journalRow {
	t.Helper()
	var rows []journalRow
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		found, err := storetx.Tx(ctx).Query(ctx,
			`SELECT type, actor_principal_id, tenant_id IS NULL FROM domain_events WHERE id = $1::uuid`, id)
		if err != nil {
			return err
		}
		defer found.Close()
		for found.Next() {
			var row journalRow
			if err := found.Scan(&row.Type, &row.Actor, &row.TenantNull); err != nil {
				return err
			}
			rows = append(rows, row)
		}
		return found.Err()
	}))
	return rows
}

func TestUserTransactionPublishesAnAuditEntryWithNoOrganizationForItsOwnUser(t *testing.T) {
	transport := auditJournalTransport(t)
	user := seedUser(t)
	envelope := auditJournalEnvelopeOf(t, user, "")
	require.Empty(t, envelope.GetTenantId())
	require.Equal(t, user, envelope.GetActorPrincipalId(), "the emitter names the entry's actor on the envelope")

	require.NoError(t, publishInUserTx(transport, user, envelope))
	require.Equal(t, []journalRow{{Type: business.AuditRecordedEventType, Actor: user, TenantNull: true}},
		journalRowsOf(t, envelope.GetId()))

	// A retry of the same entry is the same event: the function's idempotent
	// path reads the stored row back under the owner's authority, and must do so
	// for a row with no tenant.
	require.NoError(t, publishInUserTx(transport, user, envelope))
	require.Len(t, journalRowsOf(t, envelope.GetId()), 1)
}

func TestUserTransactionCannotPublishAnAuditEntryOfAnotherActor(t *testing.T) {
	transport := auditJournalTransport(t)
	user, other := seedUser(t), seedUser(t)

	elsewhere := auditJournalEnvelopeOf(t, other, "")
	requireTenantGuardRefusal(t, publishInUserTx(transport, user, elsewhere))
	require.Empty(t, journalRowsOf(t, elsewhere.GetId()))

	// No actor at all is no actor equal to the user.
	anonymous := auditJournalEnvelopeOf(t, "", "")
	requireTenantGuardRefusal(t, publishInUserTx(transport, user, anonymous))
	require.Empty(t, journalRowsOf(t, anonymous.GetId()))
}

func TestUserTransactionCannotPublishAnyOtherTypeForNoTenant(t *testing.T) {
	transport := auditJournalTransport(t)
	user := seedUser(t)
	for _, eventType := range []string{
		"documents.entry.version_minted",
		"saas.audit.recorded.v2",
		"saas.audit.recorded",
		"SAAS.AUDIT.RECORDED.V1",
	} {
		envelope := auditJournalEnvelopeOf(t, user, "")
		envelope.Type = eventType
		envelope.Id = uuid.NewString()
		requireTenantGuardRefusal(t, publishInUserTx(transport, user, envelope))
		require.Empty(t, journalRowsOf(t, envelope.GetId()), eventType)
	}
}

func TestTenantScopedTransactionStillCannotPublishForAnotherTenant(t *testing.T) {
	transport := auditJournalTransport(t)
	user := seedUser(t)
	mine, theirs := uuid.NewString(), uuid.NewString()

	// An organization-scoped entry of the right actor, for a tenant that is not
	// the transaction's signed scope.
	foreign := auditJournalEnvelopeOf(t, user, theirs)
	err := testStore.WithOrgTx(testCtx, mine, func(ctx context.Context) error {
		return transport.Publish(ctx, storetx.Tx(ctx), foreign)
	})
	requireTenantGuardRefusal(t, err)
	require.Empty(t, journalRowsOf(t, foreign.GetId()))

	// A user-scoped transaction has no tenant at all, so it cannot name one.
	named := auditJournalEnvelopeOf(t, user, mine)
	requireTenantGuardRefusal(t, publishInUserTx(transport, user, named))
	require.Empty(t, journalRowsOf(t, named.GetId()))

	// Its own tenant is still admitted, as it always was.
	own := auditJournalEnvelopeOf(t, user, mine)
	require.NoError(t, testStore.WithOrgTx(testCtx, mine, func(ctx context.Context) error {
		return transport.Publish(ctx, storetx.Tx(ctx), own)
	}))
	require.Equal(t, []journalRow{{Type: business.AuditRecordedEventType, Actor: user}}, journalRowsOf(t, own.GetId()))
}

// A tenant-scoped transaction binds no user, so the admission has no user to
// compare the actor with and does not apply: the entry's organization is its
// only way in.
func TestOrganizationTransactionWithNoUserCannotPublishATenantlessAuditEntry(t *testing.T) {
	transport := auditJournalTransport(t)
	envelope := auditJournalEnvelopeOf(t, uuid.NewString(), "")
	err := testStore.WithOrgTx(testCtx, uuid.NewString(), func(ctx context.Context) error {
		return transport.Publish(ctx, storetx.Tx(ctx), envelope)
	})
	requireTenantGuardRefusal(t, err)
	require.Empty(t, journalRowsOf(t, envelope.GetId()))
}

// The control plane's authority is unchanged: it publishes any type for no
// tenant, which is how Emit writes the entries it opens its own transaction for.
func TestControlPlaneStillPublishesForNoTenant(t *testing.T) {
	transport := auditJournalTransport(t)
	envelope := auditJournalEnvelopeOf(t, uuid.NewString(), "")
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return transport.Publish(ctx, storetx.Tx(ctx), envelope)
	}))
	require.Len(t, journalRowsOf(t, envelope.GetId()), 1)
}
