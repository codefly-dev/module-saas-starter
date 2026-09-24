//go:build !pure

package business_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"accounts/pkg/business"
)

// Solution-declared audit event types against Postgres: the audit_event_types
// rows admission writes, the foreign key an emitted event resolves through, the
// startup sync that must leave declared rows alone, and the namespace ownership
// the advisory lock and the owner-conditioned upsert enforce. The package
// database is reused across runs without truncation and audit rows are
// append-only, so every run declares a fresh namespace.

func freshAuditNamespace(t *testing.T) string {
	t.Helper()
	return "acme_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
}

func declaringFrontendWrite(id, namespace string) business.SolutionRegistrationWrite {
	return frontendWrite(id, "publisher-"+id, `{"id":"`+id+`","dashboard":{"events":[`+
		`{"name":"created","type":"`+namespace+`.item.created","description":"An item was created.",`+
		`"fields":[{"name":"count","kind":"int"},{"name":"score","kind":"number"},{"name":"stage","kind":"enum","values":["draft","final"]}]},`+
		`{"name":"login","type":"saas.auth.login"}],"metrics":[],"dashboards":[]}}`)
}

func TestSolutionAuditEvents_AdmittedAtRegistrationAndEmitted(t *testing.T) {
	clearData(t)
	namespace := freshAuditNamespace(t)
	solution := testSolutionID(t)
	eventType := business.EventType(namespace + ".item.created")

	_, err := testService.PutSolutionRegistration(testCtx, declaringFrontendWrite(solution, namespace))
	require.NoError(t, err, "register a solution declaring a typed event")

	var admitted *business.DeclaredAuditEventType
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		admitted, err = testStore.GetDeclaredAuditEventType(ctx, eventType)
		return err
	}))
	require.NotNil(t, admitted, "the declared type must be admitted")
	require.Equal(t, solution, admitted.SolutionID)
	require.Equal(t, "An item was created.", admitted.Description)
	require.Len(t, admitted.Fields, 3)

	// A startup sync reconciles the code catalog and must not retire the
	// declared type.
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.SyncAuditEventTypes(ctx, business.AuditEventCatalog())
	}))
	var rows []business.AuditEventTypeRow
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		rows, err = testStore.ListAuditEventTypes(ctx)
		return err
	}))
	var row *business.AuditEventTypeRow
	for i := range rows {
		if rows[i].Name == string(eventType) {
			row = &rows[i]
		}
	}
	require.NotNil(t, row)
	require.False(t, row.Deprecated, "the startup sync must leave a declared type active")
	require.Equal(t, business.SolutionAuditOwner(solution), row.Owner)
	require.Equal(t, string(business.CategorySolution), row.Category)
	require.Equal(t, namespace, row.Namespace)

	// The listing a dashboard author reads carries it, labelled by its owner.
	listed, err := testService.AuditEventTypes(testCtx)
	require.NoError(t, err)
	var found bool
	for _, d := range listed {
		if d.Type == eventType {
			found = true
			require.Equal(t, business.SolutionAuditOwner(solution), d.Owner)
		}
	}
	require.True(t, found, "AuditEventTypes must list the declared type")

	// The solution's principal emits its own type into a real tenant; the audit
	// row resolves through the foreign key the admitted row satisfies.
	_, org := mustUserAndOrg(t, testCtx, "declared@audit-test.com", "declared-audit", "Declared Co")
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	svc.SetAuditEmitter(emitter)
	backend := &fakeJobBackend{}
	svc.SetModuleCapabilities(backend, backend, business.ModulePrincipalRegistry{
		modulePrincSvc: {Namespaces: []string{namespace}},
	})
	caller := business.ModuleCaller{PrincipalID: modulePrincSvc, BoundOrg: org}
	fields, err := structpb.NewStruct(map[string]any{"count": 2, "score": 0.5, "stage": "final"})
	require.NoError(t, err)
	require.NoError(t, svc.ModuleEmitAuditEvent(testCtx, caller, org, string(eventType),
		modulePrincSvc, solution, "", "", fields), "emit the solution's own declared type")

	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	entries, _, _, err := testService.QueryAuditLog(testCtx, business.AuditQuery{
		OrgID: org, EventType: string(eventType), From: &past, To: &future, PageSize: 10,
	})
	require.NoError(t, err)
	require.Len(t, entries, 1, "exactly the one emitted event")
	require.Equal(t, solution, entries[0].Payload["solution"])
	require.Equal(t, business.ActorTypeAgent, entries[0].ActorType)
	require.Equal(t, 0.5, entries[0].Payload["score"])

	// Another solution's label, and a non-finite number, are refused.
	err = svc.ModuleEmitAuditEvent(testCtx, caller, org, string(eventType), modulePrincSvc, "other-solution", "", "", nil)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "another solution may not emit this type")
	nonFinite, err := structpb.NewStruct(map[string]any{"score": math.Inf(1)})
	require.NoError(t, err)
	err = svc.ModuleEmitAuditEvent(testCtx, caller, org, string(eventType), modulePrincSvc, solution, "", "", nonFinite)
	require.Equal(t, codes.InvalidArgument, status.Code(err), "a non-finite number must be refused")
}

func TestSolutionAuditEvents_NamespaceBelongsToOneSolution(t *testing.T) {
	namespace := freshAuditNamespace(t)
	first, second := testSolutionID(t), testSolutionID(t)

	_, err := testService.PutSolutionRegistration(testCtx, declaringFrontendWrite(first, namespace))
	require.NoError(t, err)

	_, err = testService.PutSolutionRegistration(testCtx, declaringFrontendWrite(second, namespace))
	require.True(t, errors.Is(err, business.ErrSolutionAuditNamespaceOwned), "err = %v, want the namespace refused", err)

	// The refusal rolled the whole write back: the second solution has no record.
	records, _, err := testService.ListSolutionRegistrations(testCtx, true)
	require.NoError(t, err)
	for _, record := range records {
		require.NotEqual(t, second, record.SolutionID, "a refused declaration must not store the registration")
	}

	// Re-registering the owner's identical declaration in a changed manifest is
	// idempotent, and a field-dropping change is refused.
	var current *business.SolutionRegistration
	for _, record := range records {
		if record.SolutionID == first {
			current = record
		}
	}
	require.NotNil(t, current)
	renamed := declaringFrontendWrite(first, namespace)
	renamed.Frontend.Manifest = strings.Replace(renamed.Frontend.Manifest, `"id":"`+first+`"`, `"id":"`+first+`","schemaVersion":1`, 1)
	renamed.ExpectedRevision = &current.Revision
	current, err = testService.PutSolutionRegistration(testCtx, renamed)
	require.NoError(t, err, "an identical declaration in a changed manifest")

	dropping := frontendWrite(first, "publisher-"+first, `{"id":"`+first+`","dashboard":{"events":[`+
		`{"name":"created","type":"`+namespace+`.item.created","fields":[{"name":"count","kind":"int"}]}],"metrics":[],"dashboards":[]}}`)
	dropping.ExpectedRevision = &current.Revision
	_, err = testService.PutSolutionRegistration(testCtx, dropping)
	require.True(t, errors.Is(err, business.ErrSolutionAuditDeclarationRejected), "err = %v, want a refused change", err)
}
