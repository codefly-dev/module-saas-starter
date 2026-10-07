//go:build !pure

package business_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"accounts/pkg/business"
	"accounts/pkg/infra/storetx"
)

// Module-declared audit types against Postgres, under the codefly harness: the
// namespace lock and owner-conditioned upsert under a real race, and the whole
// declare → emit → export path through the real store. Every run declares a
// fresh namespace, because audit rows are append-only and the package database
// is reused across runs.

func moduleDeclaringService(t *testing.T, bound map[string][]string) *business.Service {
	t.Helper()
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	svc.SetAuditEmitter(emitter)
	registry := business.ModulePrincipalRegistry{}
	for prefix, namespaces := range bound {
		registry[business.ModulePrincipalID(prefix)] = business.ModulePrincipalGrant{Prefix: prefix, Namespaces: namespaces}
	}
	backend := &fakeJobBackend{}
	svc.SetModuleAuthorityReads(currentModuleAuthority{}, nil)
	svc.SetModuleCapabilities(backend, backend, registry)
	return svc
}

func conversationDeclaration(namespace string) []business.AuditEventTypeDeclaration {
	return []business.AuditEventTypeDeclaration{{
		Type:        namespace + ".chat.conversation_shared",
		Description: "A conversation was shared.",
		Fields: []business.AuditFieldDeclaration{
			{Name: "conversation_id", Kind: "string"},
			{Name: "grantee_kind", Kind: "enum", Values: []string{"user", "team"}},
			{Name: "grantee_id", Kind: "string", PII: true},
			{Name: "withheld_turns", Kind: "int"},
		},
	}}
}

func namespaceOwners(t *testing.T, namespace string) []string {
	t.Helper()
	var owners []string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		owners, err = testStore.ListAuditEventNamespaceOwners(ctx, namespace)
		return err
	}))
	return owners
}

// Two modules the operator (mis)bound to the same namespace declare in it at the
// same moment. The namespace lock serializes them: exactly one wins, the other
// is refused as owned, and the namespace ends with one owner — never two, never
// a type owned by the loser.
func TestModuleAuditDeclarations_ConcurrentClaimsLeaveOneOwner(t *testing.T) {
	for round := 0; round < 5; round++ {
		namespace := freshAuditNamespace(t)
		first, second := testSolutionID(t), testSolutionID(t)
		bound := map[string][]string{first: {namespace}, second: {namespace}}

		var (
			wg      sync.WaitGroup
			start   = make(chan struct{})
			results = make([]error, 2)
		)
		for i, prefix := range []string{first, second} {
			wg.Add(1)
			go func(i int, prefix string) {
				defer wg.Done()
				svc := moduleDeclaringService(t, bound)
				<-start
				_, _, results[i] = svc.ModuleDeclareAuditEventTypes(testCtx,
					business.ModuleCaller{PrincipalID: business.ModulePrincipalID(prefix)}, prefix,
					conversationDeclaration(namespace))
			}(i, prefix)
		}
		close(start)
		wg.Wait()

		won := 0
		for _, err := range results {
			if err == nil {
				won++
				continue
			}
			require.Equal(t, codes.InvalidArgument, status.Code(err), "the loser is refused, not failed: %v", err)
			require.Contains(t, err.Error(), business.ErrSolutionAuditNamespaceOwned.Error())
		}
		require.Equal(t, 1, won, "round %d: exactly one declaration wins, got %v", round, results)

		owners := namespaceOwners(t, namespace)
		require.Len(t, owners, 1, "round %d: one owner, never two", round)
		winner := first
		if results[0] != nil {
			winner = second
		}
		require.Equal(t, business.SolutionAuditOwner(winner), owners[0])
	}
}

// Declare, then emit, through the real store: a declared type is accepted with
// its declared fields and stored; an undeclared field and a mistyped one are
// refused; and the field declared pii is stripped from the export a tenant
// downloads while the rest of the payload survives.
func TestModuleAuditDeclarations_DeclareEmitExport(t *testing.T) {
	clearData(t)
	namespace := freshAuditNamespace(t)
	prefix := testSolutionID(t)
	svc := moduleDeclaringService(t, map[string][]string{prefix: {namespace}})
	caller := business.ModuleCaller{PrincipalID: business.ModulePrincipalID(prefix)}
	eventType := namespace + ".chat.conversation_shared"

	admitted, _, err := svc.ModuleDeclareAuditEventTypes(testCtx, caller, prefix, conversationDeclaration(namespace))
	require.NoError(t, err)
	require.Equal(t, []business.EventType{business.EventType(eventType)}, admitted)
	// A module declares on every start: the same declaration again writes nothing.
	again, _, err := svc.ModuleDeclareAuditEventTypes(testCtx, caller, prefix, conversationDeclaration(namespace))
	require.NoError(t, err)
	require.Empty(t, again)

	_, org := mustUserAndOrg(t, testCtx, "declared@module-audit.test", "declared-module-audit", "Acme Module Audit")
	caller.BoundOrg = org
	emit := func(payload map[string]any) error {
		fields, err := structpb.NewStruct(payload)
		require.NoError(t, err)
		return svc.ModuleEmitAuditEvent(testCtx, caller, org, eventType, "system:chat", prefix, "conversation-1", "", fields)
	}

	require.NoError(t, emit(map[string]any{
		"conversation_id": "conversation-1", "grantee_kind": "team", "grantee_id": "team-7", "withheld_turns": 2,
	}), "the declared type with its declared fields")
	require.Equal(t, codes.InvalidArgument, status.Code(emit(map[string]any{"conversation_id": "c", "undeclared": "x"})))
	require.Equal(t, codes.InvalidArgument, status.Code(emit(map[string]any{"conversation_id": "c", "grantee_kind": "org"})))
	require.Equal(t, codes.InvalidArgument, status.Code(emit(map[string]any{"conversation_id": "c", "withheld_turns": 1.5})))

	body, _, _, err := svc.ExportAuditLog(testCtx, org, "json", "", eventType, nil)
	require.NoError(t, err)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(body, &rows))
	require.Len(t, rows, 1, "exactly the one accepted event is exported")
	payload := exportPayload(t, rows[0])
	require.Equal(t, "conversation-1", payload["conversation_id"])
	require.Equal(t, "team", payload["grantee_kind"])
	require.Equal(t, prefix, payload["solution"])
	_, leaked := payload["grantee_id"]
	require.False(t, leaked, "the pii field must not reach the export: %v", payload)
}

func exportPayload(t *testing.T, row map[string]any) map[string]any {
	t.Helper()
	for _, key := range []string{"payload", "Payload"} {
		if raw, ok := row[key]; ok {
			payload, ok := raw.(map[string]any)
			require.True(t, ok, "payload is %T", raw)
			return payload
		}
	}
	t.Fatal(errors.New("export row carries no payload"))
	return nil
}

// The retention class a module declares is stored on the type's row and read
// back by the one type lookup the relay classifies with; it only grows; and a
// code-owned type's row carries the catalog's class.
func TestModuleAuditDeclarations_RetentionClassIsStoredAndOnlyGrows(t *testing.T) {
	clearData(t)
	namespace := freshAuditNamespace(t)
	prefix := testSolutionID(t)
	svc := moduleDeclaringService(t, map[string][]string{prefix: {namespace}})
	caller := business.ModuleCaller{PrincipalID: business.ModulePrincipalID(prefix)}
	eventType := business.EventType(namespace + ".access.granted")
	declare := func(retention string) error {
		_, _, err := svc.ModuleDeclareAuditEventTypes(testCtx, caller, prefix,
			[]business.AuditEventTypeDeclaration{{Type: string(eventType), Retention: retention}})
		return err
	}
	stored := func() business.AuditRetentionClass {
		t.Helper()
		resolved, err := business.NewAuditEventResolver(testStore).Resolve(testCtx, eventType)
		require.NoError(t, err)
		require.True(t, resolved.Registered)
		return resolved.RetentionClass()
	}

	require.NoError(t, declare(""))
	require.Equal(t, business.RetentionContent, stored(), "saying nothing is content")
	require.NoError(t, declare("security"))
	require.Equal(t, business.RetentionSecurity, stored(), "the class may rise")
	require.Error(t, declare(""), "and may not fall, not even by omission")
	require.Equal(t, business.RetentionSecurity, stored())

	var catalogClass string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx,
			`SELECT retention_class FROM audit_event_types WHERE name = $1`, string(business.EventAuthLogin)).Scan(&catalogClass)
	}))
	require.Equal(t, string(business.RetentionSecurity), catalogClass, "the startup projection writes the catalog's class")
}
