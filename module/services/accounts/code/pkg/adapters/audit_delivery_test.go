package adapters

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"connectrpc.com/connect"
	"github.com/codefly-dev/sdk-go/receipts"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestAuditDeliveryRequiresTheModuleGrantAndDeliveryScope(t *testing.T) {
	for _, tc := range []struct {
		name        string
		crossTenant bool
		actions     string
		audience    string
		lookup      bool
		want        connect.Code
	}{
		{"headless invoke", true, `"invoke","read"`, "saas-audit", false, 0},
		{"headless lookup", true, `"invoke","read"`, "saas-audit", true, 0},
		{"tenant bound module", false, `"invoke","read"`, "saas-audit", false, connect.CodePermissionDenied},
		{"lookup cannot invoke", true, `"read"`, "saas-audit", false, connect.CodePermissionDenied},
		{"invoke cannot lookup", true, `"invoke"`, "saas-audit", true, connect.CodePermissionDenied},
		{"wrong audience", true, `"invoke","read"`, "other-service", false, connect.CodeUnauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			declared := `{"documents":{"cross_tenant":` + fmt.Sprint(tc.crossTenant) + `,"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},"tenant":"` + moduleWorkContextTenant + `","operation_audiences":{"audit":{"audience":"` + tc.audience + `","invoke_scopes":[{"resource_kind":"saas.audit-delivery","actions":[` + tc.actions + `]}],"headless_scopes":[{"resource_kind":"saas.audit-delivery","actions":[` + tc.actions + `]}]}}}}`
			installModuleOperationContextService(t, declared)
			installModuleWorkContextAuthority(t)
			issued, err := mintModuleOperationContext("documents", moduleToken(t, "documents"), "audit")
			require.NoError(t, err)
			header := make(http.Header)
			header.Set(workContextHeader, issued.GetToken())
			ctx, err := authenticateAuditDeliveryContext(t.Context(), header, tc.lookup)
			if tc.want != 0 {
				require.Equal(t, tc.want, connect.CodeOf(err))
				return
			}
			require.NoError(t, err)
			tenant, _, ok := auth.VerifiedDatabaseIdentity(ctx)
			require.True(t, ok)
			require.Equal(t, moduleWorkContextTenant, tenant)
			// A context cannot outlive the declared grant it was minted under.
			service.SetModulePrincipals(nil)
			_, err = authenticateAuditDeliveryContext(t.Context(), header, tc.lookup)
			require.Error(t, err)
		})
	}
}

func TestReadJournalBoundsTheProjectedWireAndResumesAtTheLastSentEntry(t *testing.T) {
	data := map[string]any{}
	for n := 0; n < 20000; n++ {
		data[fmt.Sprintf("key%05d", n)] = float64(0)
	}
	page := business.ModuleReadJournalResult{NextAfter: "last"}
	for n := 0; n < 12; n++ {
		page.Entries = append(page.Entries, business.ModuleJournalEntry{Position: fmt.Sprintf("p%d", n), Type: business.AuditRecordedEventType, Data: data, OccurredAt: time.Now()})
	}
	out, err := moduleJournalPageToProto("start", page)
	require.NoError(t, err)
	require.NotEmpty(t, out.Entries)
	require.Less(t, len(out.Entries), len(page.Entries))
	require.LessOrEqual(t, proto.Size(out), 3<<20)
	require.Equal(t, out.Entries[len(out.Entries)-1].Position, out.NextAfter)
	tail, err := moduleJournalPageToProto(out.NextAfter, business.ModuleReadJournalResult{Entries: page.Entries[len(out.Entries):]})
	require.NoError(t, err)
	require.Equal(t, page.Entries[len(out.Entries)].Position, tail.Entries[0].Position)
	_, err = moduleJournalPageToProto("start", business.ModuleReadJournalResult{Entries: []business.ModuleJournalEntry{{Data: map[string]any{"large": strings.Repeat("x", 4<<20)}}}})
	require.Error(t, err)
	empty, err := moduleJournalPageToProto("cursor", business.ModuleReadJournalResult{})
	require.NoError(t, err)
	require.Equal(t, "cursor", empty.NextAfter)
}

// Reuse the existing receipt fixture and module issuer; no warehouse is needed
// to prove how a committed receipt and an inconclusive lookup travel on the wire.
type auditReceiptWireStore struct{ *recordingModuleStore }

func (s *auditReceiptWireStore) ReadJournalPage(context.Context, business.JournalPageQuery) ([]business.JournalRow, error) {
	return []business.JournalRow{{Position: business.JournalPosition{Xact: 10, Seq: 1}, EventID: "event", Type: "example.recorded.v1", ContentType: "application/json", Data: []byte(`{"value":1}`), OccurredAt: time.Now()}}, nil
}

func (s *auditReceiptWireStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s *auditReceiptWireStore) RecordSourceOperationReceipt(ctx context.Context, store receipts.Store, response proto.Message) error {
	return receipts.Record(ctx, store, sourceWireTx{}, response)
}

func TestAuditDeliveryRuntimeRecoveryUsesTheOriginalInputAndRangeReceipt(t *testing.T) {
	declared := strings.ReplaceAll(headlessPrincipals, "modelservice", "saas-audit")
	declared = strings.ReplaceAll(declared, "saas-audit.profiles", "saas.audit-delivery")
	declared = strings.ReplaceAll(declared, `,"resource_ids":["profile-digest"]`, "")
	declared = strings.Replace(declared, `{"documents":{`, `{"documents":{"cross_tenant":true,`, 1)
	underlying, audit := installModuleOperationContextService(t, declared)
	registry := service.ModulePrincipals()
	svc, err := business.NewService(&auditReceiptWireStore{underlying})
	require.NoError(t, err)
	svc.SetAuditEmitter(audit)
	svc.SetModuleAuthorityReads(currentModuleAuthority{service: svc}, nil)
	svc.SetModuleCapabilities(nil, nil, registry)
	bindModuleExecution(t, svc, registry)
	WithService(svc)
	installModuleWorkContextAuthority(t)
	// The shared Connect-to-gRPC adapter must preserve the module credential.
	identity, err := mintModuleWorkContext(t, "documents", moduleToken(t, "documents"))
	require.NoError(t, err)
	capHandler := &moduleCapabilitiesConnectHandler{inner: ModuleCapabilitiesSingleton()}
	read := connect.NewRequest(&gen.ModuleReadJournalRequest{After: business.JournalStart, Types: []string{"example.recorded.v1"}})
	read.Header().Set(workContextHeader, identity.Token)
	page, err := capHandler.ReadJournal(t.Context(), read)
	require.NoError(t, err)
	require.Len(t, page.Msg.Entries, 1)
	require.Equal(t, "v1.10.1", page.Msg.NextAfter)
	read.Header().Add(workContextHeader, identity.Token)
	_, err = capHandler.ReadJournal(t.Context(), read)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	store := receipts.NewMemoryStore()
	require.NoError(t, svc.ConfigureSourceOperationReceipts(store))
	issued, err := mintModuleOperationContext("documents", moduleToken(t, "documents"), "model")
	require.NoError(t, err)
	handler := &auditConnectHandler{}
	mux := http.NewServeMux()
	mux.Handle(business.DeliverAuditEventsMethod, connect.NewUnaryHandler(business.DeliverAuditEventsMethod, handler.DeliverAuditEvents))
	mux.Handle(business.LookupDeliverAuditEventsMethod, connect.NewUnaryHandler(business.LookupDeliverAuditEventsMethod, handler.LookupDeliverAuditEvents))
	server := httptest.NewServer(mux)
	defer server.Close()
	invoke := connect.NewClient[gen.DeliverAuditEventsRequest, gen.DeliverAuditEventsResponse](server.Client(), server.URL+business.DeliverAuditEventsMethod, connect.WithProtoJSON())
	lookup := connect.NewClient[gen.DeliverAuditEventsRequest, gen.DeliverAuditEventsResponse](server.Client(), server.URL+business.LookupDeliverAuditEventsMethod, connect.WithProtoJSON())
	input := &gen.DeliverAuditEventsRequest{From: business.JournalStart, To: "v1.10.1"}
	request := func(msg *gen.DeliverAuditEventsRequest) *connect.Request[gen.DeliverAuditEventsRequest] {
		req := connect.NewRequest(proto.CloneOf(msg))
		req.Header().Set(workContextHeader, issued.Token)
		req.Header().Set(receipts.EffectIDHeaderName, "task/operations/deliver")
		req.Header().Set("Codefly-Runnable-Effect-Id", "task/operations/deliver")
		return req
	}
	_, err = lookup.CallUnary(t.Context(), request(input))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "absence must not be an HTTP 200 committed outcome")
	// This deployment is Postgres; reaching its business refusal proves that the
	// task's transport identity was accepted and normalized to the range identity.
	_, err = invoke.CallUnary(t.Context(), request(input))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	effect := business.AuditDeliveryEffectID(input.From)
	digest, err := receipts.RequestDigest(input)
	require.NoError(t, err)
	committed := &gen.DeliverAuditEventsResponse{EffectId: effect, Status: "committed", Delivered: 2}
	ctx := receipts.WithEffect(t.Context(), receipts.Effect{ID: effect, Tenant: moduleWorkContextTenant, Method: business.DeliverAuditEventsMethod, RequestDigest: digest})
	require.NoError(t, receipts.Record(ctx, store, sourceWireTx{}, committed))
	replay, err := invoke.CallUnary(t.Context(), request(input))
	require.NoError(t, err)
	require.True(t, proto.Equal(committed, replay.Msg))
	recovered, err := lookup.CallUnary(t.Context(), request(input))
	require.NoError(t, err)
	require.True(t, proto.Equal(committed, recovered.Msg))
	changed := proto.CloneOf(input)
	changed.To = "v1.10.2"
	_, err = lookup.CallUnary(t.Context(), request(changed))
	require.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	// A stamped login still lacks the operation context required by the handler.
	login := auth.WithVerifiedDatabaseIdentity(t.Context(), business.ModulePrincipalID("documents"), moduleWorkContextTenant)
	_, err = handler.DeliverAuditEvents(login, connect.NewRequest(input))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
