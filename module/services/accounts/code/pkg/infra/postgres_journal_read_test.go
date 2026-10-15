//go:build !pure

package infra_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"accounts/pkg/business"
	eventsv1 "accounts/pkg/gen/saas/events/v1"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// These tests hold the cross-tenant journal read (business.ModuleReadJournal over
// PostgresStore.ReadJournalPage) to its contract on the real database: it serves
// only rows whose writing transaction has finished, in (xact_id, seq) order, so a
// cursor never passes a row that can still appear below it; it serves every
// tenant's rows and the rows with no tenant to a cross-tenant principal and to no
// one else; and it marks the entries that are the caller's own delivery footprint.

// journalReadAuthority is the live authority read of a test that is not about it:
// the principal's declared grant, in full, current. The capability decision under
// test is the cross-tenant gate.
type journalReadAuthority struct{ service *business.Service }

func (f journalReadAuthority) LiveModuleAuthority(_ context.Context, principal, _, _ string) (*business.LiveModuleAuthority, error) {
	grant := f.service.ModulePrincipals()[principal]
	return &business.LiveModuleAuthority{
		InstallationID: "installation-current", RegistryBindingID: grant.PresenceBinding,
		Scope: business.FullInstallationScope(grant),
	}, nil
}

func (f journalReadAuthority) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

// journalReader is a Service over the real store with one module principal.
func journalReader(t *testing.T, crossTenant bool) (*business.Service, business.ModuleCaller) {
	t.Helper()
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	svc.SetModuleAuthorityReads(journalReadAuthority{service: svc}, nil)
	tenant := business.NewIDString()
	caller := business.ModuleCaller{PrincipalID: business.ModulePrincipalID("journal-reader"), BoundOrg: tenant}
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{
		caller.PrincipalID: {Prefix: "journal-reader", Tenant: tenant, CrossTenant: crossTenant},
	})
	return svc, caller
}

// freshJournalType is an event type no other test publishes, so a read of it sees
// exactly the rows this test wrote.
func freshJournalType() string {
	return "saas.test.journalread_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
}

func publishJournalRow(ctx context.Context, eventType, tenant string) (string, error) {
	transport := infra.NewPostgresEventTransport(infra.NewPostgresJobStore(testPool), testPool, "journal-read-"+uuid.NewString(), time.Minute)
	envelope := &eventsv1.EventEnvelope{
		Id: uuid.NewString(), Type: eventType, Source: "saas/test", Subject: "subject", Specversion: "1.0",
		Datacontenttype: "application/json", Time: timestamppb.Now(), Data: []byte(`{"k":"v"}`), TenantId: tenant,
	}
	return envelope.Id, transport.Publish(ctx, storetx.Tx(ctx), envelope)
}

// publishCommitted writes one row in a transaction of its own and commits it.
func publishCommitted(t *testing.T, eventType, tenant string) string {
	t.Helper()
	var id string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		id, err = publishJournalRow(ctx, eventType, tenant)
		return err
	}))
	return id
}

// openTx is a control-plane transaction held open on its own goroutine until
// commit is called, so a test can interleave other writers with it.
type openTx struct {
	steps chan func(context.Context) error
	done  chan error
	once  sync.Once
}

func openControlPlaneTx(t *testing.T) *openTx {
	t.Helper()
	tx := &openTx{steps: make(chan func(context.Context) error), done: make(chan error, 1)}
	started := make(chan struct{})
	go func() {
		tx.done <- testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			close(started)
			for step := range tx.steps {
				if err := step(ctx); err != nil {
					return err
				}
			}
			return nil
		})
	}()
	<-started
	t.Cleanup(func() { tx.once.Do(func() { close(tx.steps) }) }) // a test that failed before commit
	return tx
}

// do runs one statement's worth of work inside the open transaction and waits for it.
func (o *openTx) do(t *testing.T, fn func(context.Context) error) {
	t.Helper()
	failed := make(chan error, 1)
	o.steps <- func(ctx context.Context) error {
		err := fn(ctx)
		failed <- err
		return err
	}
	require.NoError(t, <-failed)
}

func (o *openTx) commit(t *testing.T) {
	t.Helper()
	o.once.Do(func() { close(o.steps) })
	require.NoError(t, <-o.done)
}

func readJournal(t *testing.T, svc *business.Service, caller business.ModuleCaller, after string, types ...string) business.ModuleReadJournalResult {
	t.Helper()
	page, err := svc.ModuleReadJournal(testCtx, caller, business.ModuleReadJournalInput{After: after, Types: types})
	require.NoError(t, err)
	return page
}

func eventIDs(page business.ModuleReadJournalResult) []string {
	ids := make([]string, 0, len(page.Entries))
	for _, entry := range page.Entries {
		ids = append(ids, entry.EventID)
	}
	return ids
}

// A transaction that has written a row and not committed holds the read back: a
// row with a higher seq that committed first is not served while the lower one is
// open, the cursor stops below both, and once the first commits both are served,
// the lower one first, with nothing skipped. A cursor on seq alone serves the
// higher row at once and then never returns for the lower.
func TestModuleReadJournalHoldsBehindAnOpenTransactionAndSkipsNothing(t *testing.T) {
	svc, caller := journalReader(t, true)
	eventType := freshJournalType()

	committed := publishCommitted(t, eventType, "")

	var held string
	open := openControlPlaneTx(t)
	open.do(t, func(ctx context.Context) (err error) {
		held, err = publishJournalRow(ctx, eventType, "")
		return err
	})
	later := publishCommitted(t, eventType, "") // a higher seq, committed first

	var heldSeq, laterSeq int64
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		if err := storetx.Tx(ctx).QueryRow(ctx, `SELECT seq FROM domain_events WHERE id = $1::uuid`, later).Scan(&laterSeq); err != nil {
			return err
		}
		// The open transaction's own row is invisible from here; its seq is read
		// inside it below.
		return nil
	}))
	open.do(t, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx, `SELECT seq FROM domain_events WHERE id = $1::uuid`, held).Scan(&heldSeq)
	})
	require.Less(t, heldSeq, laterSeq, "the open transaction's row holds the lower seq")

	first := readJournal(t, svc, caller, "", eventType)
	require.Equal(t, []string{committed}, eventIDs(first), "neither the open row nor the row that committed above it is served")
	require.Equal(t, first.Entries[0].Position, first.NextAfter, "next_after is the last entry's position")

	// The same read again changes nothing, and an empty read keeps its cursor.
	again := readJournal(t, svc, caller, first.NextAfter, eventType)
	require.Empty(t, again.Entries)
	require.Equal(t, first.NextAfter, again.NextAfter, "an empty page resumes where it was asked from")

	open.commit(t)

	second := readJournal(t, svc, caller, first.NextAfter, eventType)
	require.Equal(t, []string{held, later}, eventIDs(second), "after the commit both rows are served, the lower one first")
	require.Equal(t, second.Entries[1].Position, second.NextAfter)

	// Everything, from the beginning, is the three rows once each, in order, and
	// "start" and "" both mean the beginning.
	all := readJournal(t, svc, caller, "", eventType)
	require.Equal(t, []string{committed, held, later}, eventIDs(all))
	require.Equal(t, eventIDs(all), eventIDs(readJournal(t, svc, caller, business.JournalStart, eventType)))
	require.Equal(t, business.JournalStart, readJournal(t, svc, caller, business.JournalStart, freshJournalType()).NextAfter,
		"an empty page from the start resumes at the start")
	require.Empty(t, readJournal(t, svc, caller, second.NextAfter, eventType).Entries)
}

// The order is the writing transaction's, then seq. A transaction that took its id
// first and inserted last sorts before one that took a later id and committed
// sooner, even though its seq is higher; and the later one is held until the
// earlier one is done, because the earlier one can still commit below it.
func TestModuleReadJournalOrdersByTransactionThenSeq(t *testing.T) {
	svc, caller := journalReader(t, true)
	eventType := freshJournalType()

	open := openControlPlaneTx(t)
	open.do(t, func(ctx context.Context) error { // the id is taken here, the row is written later
		var xact string
		return storetx.Tx(ctx).QueryRow(ctx, `SELECT pg_current_xact_id()::text`).Scan(&xact)
	})
	later := publishCommitted(t, eventType, "") // a later id, a lower seq, committed first
	require.Empty(t, readJournal(t, svc, caller, "", eventType).Entries, "held behind the transaction that has the earlier id")

	var held string
	open.do(t, func(ctx context.Context) (err error) {
		held, err = publishJournalRow(ctx, eventType, "")
		return err
	})
	require.Empty(t, readJournal(t, svc, caller, "", eventType).Entries, "still open")
	open.commit(t)

	page := readJournal(t, svc, caller, "", eventType)
	require.Equal(t, []string{held, later}, eventIDs(page), "the earlier transaction first, though its seq is higher")
	var seqs [2]int64
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		for i, id := range []string{held, later} {
			if err := storetx.Tx(ctx).QueryRow(ctx, `SELECT seq FROM domain_events WHERE id = $1::uuid`, id).Scan(&seqs[i]); err != nil {
				return err
			}
		}
		return nil
	}))
	require.Greater(t, seqs[0], seqs[1])
}

// Paging by position over rows written by several transactions neither repeats nor
// skips a row, whatever the page size, and the page bound is held.
func TestModuleReadJournalPagesWithoutRepeatOrSkip(t *testing.T) {
	svc, caller := journalReader(t, true)
	eventType := freshJournalType()
	var want []string
	for range 7 {
		want = append(want, publishCommitted(t, eventType, ""))
	}
	var got []string
	after := ""
	for range 10 {
		page, err := svc.ModuleReadJournal(testCtx, caller, business.ModuleReadJournalInput{After: after, Limit: 3, Types: []string{eventType}})
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Entries), 3)
		if len(page.Entries) == 0 {
			break
		}
		got = append(got, eventIDs(page)...)
		after = page.NextAfter
	}
	require.Equal(t, want, got)
}

// The page is bounded in bytes as well as rows: the first row is always served so
// a row larger than the bound still moves the cursor, and a row is served only
// while the bytes before it fit.
func TestReadJournalPageBoundsTheBytesOfAPage(t *testing.T) {
	eventType := freshJournalType()
	body := `{"pad":"` + strings.Repeat("x", 600) + `"}`
	for range 3 {
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			envelope := &eventsv1.EventEnvelope{
				Id: uuid.NewString(), Type: eventType, Source: "saas/test", Specversion: "1.0",
				Datacontenttype: "application/json", Time: timestamppb.Now(), Data: []byte(body),
			}
			return auditJournalTransport(t).Publish(ctx, storetx.Tx(ctx), envelope)
		}))
	}
	read := func(maxBytes int) (rows []business.JournalRow) {
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) (err error) {
			rows, err = testStore.ReadJournalPage(ctx, business.JournalPageQuery{Types: []string{eventType}, Limit: 100, MaxBytes: maxBytes})
			return err
		}))
		return rows
	}
	require.Len(t, read(1), 1, "the first row is always served")
	require.Len(t, read(len(body)), 1, "the next row would exceed the byte bound")
	require.Len(t, read(2*len(body)), 2)
	require.Len(t, read(0), 3, "no bound")
}

// The read refuses to run outside a control-plane transaction instead of falling
// back to the request pool, where an empty answer would look like a quiet journal.
func TestJournalReadNeedsAControlPlaneTransaction(t *testing.T) {
	_, err := testStore.ReadJournalPage(testCtx, business.JournalPageQuery{Types: []string{freshJournalType()}, Limit: 1})
	require.Error(t, err)
	_, err = testStore.CountJournalRange(testCtx, business.JournalPageQuery{Types: []string{freshJournalType()}}, 1)
	require.Error(t, err)
	_, err = testStore.JournalPositionVisible(testCtx, business.JournalPosition{Xact: 1})
	require.Error(t, err)
}

// Every tenant's rows and the rows with no tenant are served to a cross-tenant
// principal, in one read; a principal that is not cross-tenant is refused. The
// tenant-scoped read of the same journal, by contrast, sees one tenant.
func TestModuleReadJournalServesEveryTenantAndNoTenant(t *testing.T) {
	svc, caller := journalReader(t, true)
	eventType := freshJournalType()
	orgX, orgY := seedOrg(t, seedUser(t)), seedOrg(t, seedUser(t))

	x := publishCommitted(t, eventType, orgX)
	none := publishCommitted(t, eventType, "")
	y := publishCommitted(t, eventType, orgY)

	page := readJournal(t, svc, caller, "", eventType)
	require.Equal(t, []string{x, none, y}, eventIDs(page))
	require.Equal(t, []string{orgX, "", orgY}, []string{
		page.Entries[0].OrganizationID, page.Entries[1].OrganizationID, page.Entries[2].OrganizationID,
	}, "the row with no tenant is served with an empty organization")
	for _, entry := range page.Entries {
		require.Equal(t, eventType, entry.Type)
		require.Equal(t, map[string]any{"k": "v"}, entry.Data)
		require.False(t, entry.DeliveryMachinery)
		require.WithinDuration(t, time.Now(), entry.OccurredAt, time.Minute)
		require.True(t, strings.HasPrefix(entry.Position, "v1."), entry.Position)
	}

	// The same rows through a tenant's own transaction: one tenant's.
	var seen int
	require.NoError(t, testStore.WithOrgTx(testCtx, orgX, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx, `SELECT count(*) FROM domain_events WHERE type = $1`, eventType).Scan(&seen)
	}))
	require.Equal(t, 1, seen)

	// Not cross-tenant: refused, and the refusal is the gate's, not an empty page.
	bound, boundCaller := journalReader(t, false)
	_, err := bound.ModuleReadJournal(testCtx, boundCaller, business.ModuleReadJournalInput{Types: []string{eventType}})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	_, err = svc.ModuleReadJournal(testCtx, business.ModuleCaller{PrincipalID: business.ModulePrincipalID("unregistered")}, business.ModuleReadJournalInput{Types: []string{eventType}})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
}

// The positions the read issues are the ones it accepts, and one it did not issue
// is refused.
func TestModuleReadJournalRefusesAMalformedPosition(t *testing.T) {
	svc, caller := journalReader(t, true)
	_, err := svc.ModuleReadJournal(testCtx, caller, business.ModuleReadJournalInput{After: "v1.12", Types: []string{freshJournalType()}})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
}

// delivery_machinery is computed here from the entries the host's own emitters
// write, never from a payload the caller parses: the mint of an operation context
// and the exchange on one of the caller's delivery bindings, and the caller's own
// Work Context mint, and nothing else.
func TestModuleReadJournalMarksTheCallersDeliveryMachinery(t *testing.T) {
	warehouse := &warehouseStorage{}
	audit, err := business.NewAuditService(business.AuditServiceConfig{
		Backend: business.AuditBackendWarehouse, Store: testStore, Producer: testStore,
		Transport: auditJournalTransport(t), Warehouse: warehouse,
	})
	require.NoError(t, err)
	svc, caller := journalReader(t, true)
	svc.SetAuditService(audit)
	require.NoError(t, svc.VerifyAuditWiring())

	org := seedOrg(t, seedUser(t))
	otherModule := business.ModulePrincipalID("some-other-module")
	execution := business.ExecutionIdentity{
		Namespace: "ns", ServiceAccount: "sa", PodName: "pod", PodUID: uuid.NewString(), ContainerName: "c", Running: business.RunningDigest("sha256:" + strings.Repeat("a", 64)), Incarnation: 1,
	}
	authority := func(principal string) business.ModuleWorkContextAuthority {
		return business.ModuleWorkContextAuthority{PrincipalID: principal, Tenant: org, Execution: execution}
	}
	operationMint := func(binding string) business.ModuleOperationContextAuthority {
		return business.ModuleOperationContextAuthority{
			ModuleWorkContextAuthority: authority(caller.PrincipalID), BindingID: binding, Audience: "saas-audit",
			Scopes: []business.ModuleOperationScope{{ResourceKind: "saas.audit-delivery", Actions: []string{"invoke"}}},
		}
	}
	exchange := func(binding string) business.DelegatedAudienceExchangeObservation {
		return business.DelegatedAudienceExchangeObservation{
			Caller: caller, ActorID: uuid.NewString(), Tenant: org, BindingKind: "operation", BindingID: binding,
			Audience: "saas-audit", Outcome: business.DelegatedAudienceExchangeIssued,
		}
	}

	head := drainJournal(t, svc, caller, "", business.AuditRecordedEventType).next

	// In the order written; the wanted flag beside each.
	steps := []struct {
		name  string
		event business.EventType
		want  bool
		do    func() error
	}{
		{"operation context for a delivery binding", business.EventModuleOperationContextMint, true,
			func() error {
				return svc.RecordModuleOperationContextMint(testCtx, "journal-reader", operationMint("delivery-binding"))
			}},
		{"operation context for another binding", business.EventModuleOperationContextMint, false,
			func() error {
				return svc.RecordModuleOperationContextMint(testCtx, "journal-reader", operationMint("customer-binding"))
			}},
		{"exchange on a delivery binding", business.EventDelegatedAudienceExchange, true,
			func() error { svc.ObserveDelegatedAudienceExchange(testCtx, exchange("delivery-binding")); return nil }},
		{"exchange on another binding", business.EventDelegatedAudienceExchange, false,
			func() error { svc.ObserveDelegatedAudienceExchange(testCtx, exchange("customer-binding")); return nil }},
		{"the caller's own Work Context mint", business.EventModuleWorkContextMint, true,
			func() error {
				return svc.RecordModuleWorkContextMint(testCtx, "journal-reader", authority(caller.PrincipalID))
			}},
		{"another module's Work Context mint", business.EventModuleWorkContextMint, false,
			func() error {
				return svc.RecordModuleWorkContextMint(testCtx, "some-other-module", authority(otherModule))
			}},
		// An entry of another type that happens to carry a delivery binding's id is
		// not the caller's footprint: the type decides, not a field's value.
		{"an ordinary entry that names a delivery binding", business.EventSolutionHostBindingApplied, false,
			func() error {
				return testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
					return audit.EmitTx(ctx, business.AuditEntry{
						ID: uuid.NewString(), EventType: business.EventSolutionHostBindingApplied, ActorID: uuid.NewString(),
						ActorType: business.ActorTypeSystem, Resource: "solution", ResourceID: uuid.NewString(), OrgID: org,
						Payload: map[string]any{
							"binding_id": "delivery-binding", "solution_id": "example", "generation": int64(1),
							"digest": "sha256:" + strings.Repeat("b", 64), "release": "r1", "removed": false, "routes": []any{},
						},
						CreatedAt: time.Now().UTC(),
					})
				})
			}},
	}
	for _, step := range steps {
		require.NoError(t, step.do(), step.name)
	}

	read, err := svc.ModuleReadJournal(testCtx, caller, business.ModuleReadJournalInput{
		After: head, Types: []string{business.AuditRecordedEventType}, DeliveryBindings: []string{"delivery-binding"},
	})
	require.NoError(t, err)
	var mine []business.ModuleJournalEntry
	for _, entry := range read.Entries {
		if entry.OrganizationID == org {
			mine = append(mine, entry)
		}
	}
	require.Len(t, mine, len(steps))
	for i, step := range steps {
		require.Equal(t, string(step.event), mine[i].Data["event_type"], step.name)
		require.Equal(t, step.want, mine[i].DeliveryMachinery, step.name)
	}

	// With no delivery bindings named, only the caller's own mint is marked.
	unbound, err := svc.ModuleReadJournal(testCtx, caller, business.ModuleReadJournalInput{After: head, Types: []string{business.AuditRecordedEventType}})
	require.NoError(t, err)
	var flagged []string
	for _, entry := range unbound.Entries {
		if entry.OrganizationID == org && entry.DeliveryMachinery {
			flagged = append(flagged, fmt.Sprint(entry.Data["event_type"]))
		}
	}
	require.Equal(t, []string{string(business.EventModuleWorkContextMint)}, flagged)
}

type drained struct {
	entries []business.ModuleJournalEntry
	next    string
}

// drainJournal reads to the end of the visible journal for the types.
func drainJournal(t *testing.T, svc *business.Service, caller business.ModuleCaller, after string, types ...string) drained {
	t.Helper()
	out := drained{next: after}
	for range 1000 {
		page, err := svc.ModuleReadJournal(testCtx, caller, business.ModuleReadJournalInput{After: out.next, Limit: business.ModuleJournalMaxPage, Types: types})
		require.NoError(t, err)
		if len(page.Entries) == 0 {
			return out
		}
		out.entries = append(out.entries, page.Entries...)
		out.next = page.NextAfter
	}
	t.Fatal("the journal did not drain")
	return out
}
