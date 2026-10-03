package business

import (
	"context"
	"errors"
	"strings"
	"testing"

	"accounts/pkg/events"
	eventsv1 "accounts/pkg/gen/saas/events/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

// An audit event type a solution or a composed module declares reaches an
// outbound webhook exactly when its producer declared it external and the
// operator granted its namespace external delivery — and never otherwise.
//
// The defect these cover: every declared type was silently undeliverable. The
// emitter published no domain event for one, so the relay had nothing to fan
// out, while CreateSubscription accepted a subscription to it and TestWebhook
// delivered a test for it. A customer got no indication that the endpoint would
// never receive the type.

// visibilityStore answers the three reads the audit write path makes, with one
// declared type admitted. The transaction wrappers run fn inline: no statement
// here needs a database.
type visibilityStore struct {
	Store
	declared *DeclaredAuditEventType
	inserted []AuditEntry
}

func (visibilityStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (visibilityStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *visibilityStore) GetDeclaredAuditEventType(_ context.Context, t EventType) (*DeclaredAuditEventType, error) {
	if s.declared == nil || s.declared.Type != t {
		return nil, nil
	}
	return s.declared, nil
}

func (s *visibilityStore) InsertAuditEvent(_ context.Context, entry AuditEntry) error {
	s.inserted = append(s.inserted, entry)
	return nil
}

// recordingTransport captures what the emitter publishes. Only Publish is ever
// called on the emit path; the rest of the interface belongs to the relay.
type recordingTransport struct {
	events.Transport
	published []*eventsv1.EventEnvelope
}

func (r *recordingTransport) Publish(_ context.Context, _ events.TxHandle, e *eventsv1.EventEnvelope) error {
	r.published = append(r.published, e)
	return nil
}

// silentProducer satisfies the emitter's required job producer. The external
// tee is off in these tests, so nothing is ever enqueued.
type silentProducer struct{}

func (silentProducer) EnqueueJob(context.Context, *jobsv1.EnqueueJobRequest) (*jobsv1.EnqueueJobResponse, error) {
	return &jobsv1.EnqueueJobResponse{}, nil
}

func declaredType(namespace, visibility string) *DeclaredAuditEventType {
	return &DeclaredAuditEventType{
		Type:       EventType(namespace + ".item.created"),
		Namespace:  namespace,
		SolutionID: namespace,
		Visibility: visibility,
		Fields:     []PayloadField{{Name: "count", Kind: FieldInt}},
	}
}

func emitOne(t *testing.T, store *visibilityStore, eventType EventType) *recordingTransport {
	t.Helper()
	transport := &recordingTransport{}
	emitter, err := NewDurableAuditEmitter(store, silentProducer{}, WithDomainEventTransport(transport))
	if err != nil {
		t.Fatalf("NewDurableAuditEmitter: %v", err)
	}
	entry := AuditEntry{
		EventType: eventType,
		Resource:  "item",
		OrgID:     "11111111-1111-1111-1111-111111111111",
		ActorID:   "22222222-2222-2222-2222-222222222222",
		ActorType: ActorTypeSystem,
		Payload:   map[string]any{"solution": store.declared.SolutionID, "count": 1},
	}
	if err := emitter.EmitTx(t.Context(), entry); err != nil {
		t.Fatalf("EmitTx: %v", err)
	}
	if len(store.inserted) != 1 {
		t.Fatalf("audit rows written = %d, want 1", len(store.inserted))
	}
	return transport
}

// The reported defect, at the emitter: a declared type produced an audit row
// and no domain event, so the relay had nothing to fan out — no delivery row,
// no error, no log line.
func TestDeclaredAuditTypeIsPublishedOnlyWhenExternal(t *testing.T) {
	for _, tc := range []struct {
		visibility string
		published  int
	}{
		{AuditVisibilityExternal, 1},
		{AuditVisibilityTenant, 0},
	} {
		t.Run(tc.visibility, func(t *testing.T) {
			declared := declaredType("example", tc.visibility)
			store := &visibilityStore{declared: declared}
			transport := emitOne(t, store, declared.Type)
			if len(transport.published) != tc.published {
				t.Fatalf("domain events published = %d, want %d", len(transport.published), tc.published)
			}
			if tc.published == 1 && transport.published[0].GetType() != string(declared.Type) {
				t.Fatalf("published type = %q, want %q", transport.published[0].GetType(), declared.Type)
			}
		})
	}
}

// The code-owned catalog keeps answering for its own types, from the composed
// event catalog rather than from a declaration: the fix must not make an
// external platform type depend on a row that only a declaration writes.
func TestCatalogAuditTypeStaysPublished(t *testing.T) {
	store := &visibilityStore{declared: declaredType("example", AuditVisibilityTenant)}
	transport := emitOne(t, store, EventUserCreated)
	if len(transport.published) != 1 {
		t.Fatalf("domain events published for %s = %d, want 1", EventUserCreated, len(transport.published))
	}
}

// The resolver is the one lookup both gates read, so the answer has to be right
// for a type from either half of the registry.
func TestResolvedAuditEventExternallyDeliverable(t *testing.T) {
	declared := declaredType("example", AuditVisibilityExternal)
	store := &visibilityStore{declared: declared}
	resolver := NewAuditEventResolver(store)
	for _, tc := range []struct {
		eventType   EventType
		deliverable bool
		why         string
	}{
		{declared.Type, true, "declared external"},
		{EventUserCreated, true, "the composed catalog declares every platform audit type external"},
		{"example.item.closed", false, "not registered at all"},
	} {
		resolved, err := resolver.Resolve(t.Context(), tc.eventType)
		if err != nil {
			t.Fatalf("resolve %s: %v", tc.eventType, err)
		}
		if got := resolved.ExternallyDeliverable(); got != tc.deliverable {
			t.Fatalf("%s deliverable = %v, want %v (%s)", tc.eventType, got, tc.deliverable, tc.why)
		}
	}
}

// A tenant-visibility declared type resolves as registered, so it is redacted
// and validated exactly like any other — it is only delivery that it is
// excluded from.
func TestTenantVisibleDeclaredTypeStaysRegistered(t *testing.T) {
	declared := declaredType("example", AuditVisibilityTenant)
	store := &visibilityStore{declared: declared}
	resolved, err := NewAuditEventResolver(store).Resolve(t.Context(), declared.Type)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !resolved.Registered {
		t.Fatal("a tenant-visible declared type must still be a registered type")
	}
	if resolved.ExternallyDeliverable() {
		t.Fatal("a tenant-visible declared type must not be deliverable")
	}
}

// Subscribing to a name this deployment would never deliver is refused at the
// call that makes it, naming every offending name and why — instead of storing
// a row that can never fire.
func TestRefuseUndeliverableWebhookEvents(t *testing.T) {
	declared := declaredType("example", AuditVisibilityTenant)
	store := &visibilityStore{declared: declared}
	svc, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if err := svc.refuseUndeliverableWebhookEvents(t.Context(), []string{string(EventUserCreated)}); err != nil {
		t.Fatalf("a platform type must be subscribable: %v", err)
	}

	err = svc.refuseUndeliverableWebhookEvents(t.Context(), []string{
		string(EventUserCreated),
		string(declared.Type),
		// One of the four names the webhook form used to offer that the
		// catalog never had: the catalog has saas.invitation.*.
		"saas.invite.sent",
	})
	if !errors.Is(err, ErrWebhookEventTypeUndeliverable) {
		t.Fatalf("error = %v, want ErrWebhookEventTypeUndeliverable", err)
	}
	for _, want := range []string{string(declared.Type), "saas.invite.sent"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), string(EventUserCreated)) {
		t.Fatalf("error %q names a type that IS deliverable", err)
	}

	// Once the same type is declared external, the very same name is accepted.
	store.declared = declaredType("example", AuditVisibilityExternal)
	if err := svc.refuseUndeliverableWebhookEvents(t.Context(), []string{string(declared.Type)}); err != nil {
		t.Fatalf("an external declared type must be subscribable: %v", err)
	}
}

// A manifest declares a type's visibility beside its fields. The default is the
// one that keeps events inside the platform, and a value the vocabulary does
// not name is refused rather than read as the default — a misspelt "External"
// that silently meant "tenant" would be the same undeliverable-and-silent
// failure by another route.
func TestParseDeclaredAuditEventTypes_ReadsVisibility(t *testing.T) {
	event := func(visibility string) string {
		return `{"name":"created","type":"acme.item.created"` + visibility +
			`,"fields":[{"name":"count","kind":"int"}]}`
	}
	for name, tc := range map[string]struct {
		manifest string
		want     string
	}{
		"external": {event(`,"visibility":"external"`), AuditVisibilityExternal},
		"tenant":   {event(`,"visibility":"tenant"`), AuditVisibilityTenant},
		"omitted":  {event(``), AuditVisibilityTenant},
		"empty":    {event(`,"visibility":""`), AuditVisibilityTenant},
	} {
		declared, err := ParseDeclaredAuditEventTypes("acme", declaringManifest("acme", tc.manifest))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(declared) != 1 || declared[0].Visibility != tc.want {
			t.Fatalf("%s: visibility = %#v, want %q", name, declared, tc.want)
		}
	}
	for _, bad := range []string{"External", "internal", "public", "EXTERNAL"} {
		_, err := ParseDeclaredAuditEventTypes("acme",
			declaringManifest("acme", event(`,"visibility":"`+bad+`"`)))
		if !errors.Is(err, ErrSolutionAuditDeclarationRejected) {
			t.Fatalf("visibility %q: error = %v, want a rejection", bad, err)
		}
	}
}

// The operator's half of the decision is refused where a composition can still
// be corrected — at boot, naming both the namespace and what it is not among —
// rather than at the registration that trips over it.
func TestParseModulePrincipalRegistry_ExternalNamespaceMustBeBound(t *testing.T) {
	const tenant = `"tenant":"11111111-1111-1111-1111-111111111111"`
	if _, err := ParseModulePrincipalRegistry(
		`{"example":{"namespaces":["example"],"external_namespaces":["example"],` + tenant + `}}`,
	); err != nil {
		t.Fatalf("a granted namespace that is bound must parse: %v", err)
	}
	_, err := ParseModulePrincipalRegistry(
		`{"example":{"namespaces":["example"],"external_namespaces":["other"],` + tenant + `}}`,
	)
	if err == nil {
		t.Fatal("an external namespace outside the bound namespaces must refuse the registry")
	}
	for _, want := range []string{"other", "example"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err, want)
		}
	}
}

// externallyBound is the operator's binding for a solution: every namespace
// bound, and the subset granted external delivery.
func externallyBound(t *testing.T, store *declaredAuditStore, solution string, namespaces, external []string) *Service {
	t.Helper()
	svc, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	withCurrentAuthority(svc)
	withCurrentAuthority(svc)
	svc.SetModulePrincipals(ModulePrincipalRegistry{
		ModulePrincipalID(solution): {Prefix: solution, Namespaces: namespaces, ExternalNamespaces: external},
	})
	return svc
}

const externalDeclaredEvent = `{"name":"created","type":"acme.item.created","visibility":"external",
	"fields":[{"name":"count","kind":"int"}]}`

const tenantDeclaredEvent = `{"name":"created","type":"acme.item.created","visibility":"tenant",
	"fields":[{"name":"count","kind":"int"}]}`

// Both keys are required, and the one the producer cannot supply is refused at
// the registration that declared it rather than dropped silently at delivery.
func TestAdmission_ExternalVisibilityNeedsTheOperatorsGrant(t *testing.T) {
	store := newDeclaredAuditStore()
	svc := externallyBound(t, store, "acme", []string{"acme"}, nil)
	_, err := registerDeclaring(t, svc, "acme", nil, externalDeclaredEvent)
	if !errors.Is(err, ErrSolutionAuditNamespaceNotExternal) || !errors.Is(err, ErrSolutionAuditDeclarationRejected) {
		t.Fatalf("err = %v, want the ungranted external namespace refused", err)
	}
	for _, want := range []string{"acme.item.created", "acme", "external"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err, want)
		}
	}
	if _, stored := store.registrations["acme"]; stored || len(store.rows) != 0 {
		t.Fatal("a refused declaration must store nothing")
	}

	// The same namespace, without the external declaration, needs no grant.
	store = newDeclaredAuditStore()
	svc = externallyBound(t, store, "acme", []string{"acme"}, nil)
	if _, err := registerDeclaring(t, svc, "acme", nil, tenantDeclaredEvent); err != nil {
		t.Fatalf("a tenant-visible declaration needs no external grant: %v", err)
	}
	if got := store.rows["acme.item.created"].declared.Visibility; got != AuditVisibilityTenant {
		t.Fatalf("admitted visibility = %q, want %q", got, AuditVisibilityTenant)
	}

	// With the grant, the external declaration is admitted and the row carries
	// the visibility the delivery gates read.
	store = newDeclaredAuditStore()
	svc = externallyBound(t, store, "acme", []string{"acme"}, []string{"acme"})
	if _, err := registerDeclaring(t, svc, "acme", nil, externalDeclaredEvent); err != nil {
		t.Fatalf("granted external declaration: %v", err)
	}
	admitted := store.rows["acme.item.created"].declared
	if !admitted.ExternallyDeliverable() {
		t.Fatalf("admitted = %#v, want an externally deliverable type", admitted)
	}
}

// Visibility is fixed at admission. Both directions are refused: narrowing
// would silently stop deliveries to endpoints already subscribed, widening
// would start sending out facts under a name a tenant subscribed to when it
// meant something else.
func TestAdmission_VisibilityIsImmutable(t *testing.T) {
	for name, tc := range map[string]struct{ first, second string }{
		"widening":  {tenantDeclaredEvent, externalDeclaredEvent},
		"narrowing": {externalDeclaredEvent, tenantDeclaredEvent},
	} {
		store := newDeclaredAuditStore()
		svc := externallyBound(t, store, "acme", []string{"acme"}, []string{"acme"})
		record, err := registerDeclaring(t, svc, "acme", nil, tc.first)
		if err != nil {
			t.Fatalf("%s: first declaration: %v", name, err)
		}
		revision := record.Revision
		_, err = registerDeclaring(t, svc, "acme", &revision, tc.second)
		if !errors.Is(err, ErrSolutionAuditDeclarationRejected) {
			t.Fatalf("%s: err = %v, want the visibility change refused", name, err)
		}
		if !strings.Contains(err.Error(), "acme.item.created") {
			t.Fatalf("%s: error %q does not name the type", name, err)
		}
		// The refusal rolls the whole registration back, so the admitted type
		// keeps the visibility it was admitted with.
		if got := store.rows["acme.item.created"].declared.Visibility; !strings.Contains(tc.first, got) {
			t.Fatalf("%s: admitted visibility = %q, want the one in %q", name, got, tc.first)
		}
	}

	// Re-declaring the same visibility is still idempotent.
	store := newDeclaredAuditStore()
	svc := externallyBound(t, store, "acme", []string{"acme"}, []string{"acme"})
	record, err := registerDeclaring(t, svc, "acme", nil, externalDeclaredEvent)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	puts := store.puts
	revision := record.Revision
	if _, err := registerDeclaring(t, svc, "acme", &revision, externalDeclaredEvent); err != nil {
		t.Fatalf("re-declaration: %v", err)
	}
	if store.puts != puts {
		t.Fatalf("puts = %d, want %d — an identical re-declaration writes nothing", store.puts, puts)
	}
}
