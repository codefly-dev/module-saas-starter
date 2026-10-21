package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"accounts/internal/deliverytest"
	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// This fixture derives the registry from signed applied manifests and durable
// envelope history, using the production reconciler. It never injects provenance.
type installedReadStore struct {
	*readProjectionStore
	business.WorkContextAuthorityStore
	business.SolutionDeliveryStore
	business.ModuleEnvelopeStore
	records    []*business.SolutionHostBindingRecord
	deliveries map[string]*business.SolutionDeliveryRecord
	envelope   *business.ModuleEnvelopeRecord
	live       map[string]*business.LiveModuleAuthority
	broad      bool
	liveErr    error
}

func (f *installedReadStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (f *installedReadStore) WithControlPlaneReadSnapshot(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (f *installedReadStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}
func (f *installedReadStore) LiveDeclaredBindings(context.Context) ([]business.LiveDeclaredBinding, error) {
	return nil, nil
}
func (f *installedReadStore) GetPlatformRole(context.Context, string) (string, error) { return "", nil }
func (f *installedReadStore) GetOrgMembership(context.Context, string, string) (*gen.OrgMembership, error) {
	return &gen.OrgMembership{UserId: readOwner, Role: gen.OrgRole_ORG_ROLE_MEMBER}, nil
}
func (f *installedReadStore) ListSolutionHostBindings(context.Context) ([]*business.SolutionHostBindingRecord, error) {
	return f.records, nil
}
func (f *installedReadStore) ReadDeliveredDocument(_ context.Context, binding string, _ uint64) (*business.SolutionDeliveryRecord, error) {
	return f.deliveries[binding], nil
}
func (f *installedReadStore) LatestModuleEnvelopeRecord(context.Context) (*business.ModuleEnvelopeRecord, error) {
	return f.envelope, nil
}
func (f *installedReadStore) LatestModuleEnvelopeRevision(context.Context) (uint64, error) {
	return f.envelope.Revision, nil
}
func (f *installedReadStore) ModuleEnvelopeHistory(context.Context, uint64) ([]*business.ModuleEnvelopeRecord, error) {
	return []*business.ModuleEnvelopeRecord{f.envelope}, nil
}
func (f *installedReadStore) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}
func (f *installedReadStore) LiveModuleAuthority(_ context.Context, principal, org, _ string) (*business.LiveModuleAuthority, error) {
	if f.liveErr != nil {
		return nil, f.liveErr
	}
	if org != readOrg || f.live[principal] == nil {
		return nil, business.ErrModuleInstallationInactive
	}
	return f.live[principal], nil
}
func (f *installedReadStore) ResolveWorkContextAuthority(_ context.Context, _, _, actor string, permissions []business.WorkContextPermission) (*business.WorkContextAuthorityFacts, error) {
	if actor != "" {
		return nil, business.NewStoreError(errors.New("module has no personal grants"), business.ErrTypePermission)
	}
	for _, p := range permissions {
		if p.ResourceKind == "documents" && (!p.ContentRead && !f.broad || len(f.grants[readOwner]) == 0) {
			return nil, business.NewStoreError(errors.New("collection reader only"), business.ErrTypePermission)
		}
	}
	return &business.WorkContextAuthorityFacts{OrganizationRevision: 7, PrincipalRevision: 3}, nil
}
func (f *installedReadStore) CheckAccess(_ context.Context, subject string, _ gen.SubjectKind, resource, id, action string) (bool, string, error) {
	if subject != readOwner || resource != "documents" || action != "read" {
		return false, "", nil
	}
	for _, grant := range f.grants[subject] {
		if grant.NodeId == id {
			return true, id, nil
		}
	}
	return false, "", nil
}

func (f *installedReadStore) RecordScopeNodeID(_ context.Context, _, id string) (string, error) {
	return id, nil
}

type installedReadSeals struct {
	*sealAnswersFake
	fixture *installedReadStore
}

func (s installedReadSeals) WorkContextAudienceInstallation(_ context.Context, _, audience string) (string, error) {
	return s.fixture.live[business.ModulePrincipalID(audience)].InstallationID, nil
}

func installedReadFixture(t *testing.T) (*installedReadStore, *WorkContextAuthorityServer) {
	t.Helper()
	previous, previousAuthority := service, workContextSingleton
	t.Cleanup(func() { service, workContextSingleton = previous, previousAuthority; SetInternalToken("") })
	f := &installedReadStore{readProjectionStore: &readProjectionStore{grants: map[string][]*gen.AccessibleScope{readOwner: {{NodeId: "boundary-a"}, {NodeId: "boundary-b"}}}, sources: []*business.DatasourceSource{{ID: sourceA, OrgID: readOrg, Provider: "upload", BoundaryNodeID: "boundary-a"}, {ID: sourceB, OrgID: readOrg, Provider: "upload", BoundaryNodeID: "boundary-b"}}}, deliveries: map[string]*business.SolutionDeliveryRecord{}, live: map[string]*business.LiveModuleAuthority{}}
	svc, err := business.NewService(f)
	require.NoError(t, err)
	service = svc
	verifier := deliverytest.Verifier(t, map[string][]string{"fixture": {"acme"}})
	svc.SetSolutionDelivery(verifier, f, nil)
	svc.SetModuleEnvelopeStore(f)
	svc.SetModuleAuthorityReads(f, nil)
	principals := map[string]any{}
	for _, prefix := range []string{"reader", "documents", "compute"} {
		authority := &solutionhost.Authority{Principal: prefix}
		grant := map[string]any{"tenant": readOrg}
		if prefix == "documents" {
			authority.ResourceKinds = []string{"documents"}
			grant["resources"] = []string{"documents"}
		}
		if prefix == "reader" {
			authority.Bindings = []solutionhost.AuthorityBinding{{ID: "evidence", Audience: "documents", Operations: map[string][]string{"invoke": {"documents:read"}}}, {ID: "execution", Audience: "compute", Operations: map[string][]string{"invoke": {"profiles:read", "profiles:invoke"}, "lookup": {"profiles:read"}}}}
			grant["operation_audiences"] = map[string]any{"evidence": map[string]any{"audience": "documents", "invoke_scopes": []any{map[string]any{"resource_kind": "documents", "actions": []string{"read"}}}}, "execution": map[string]any{"audience": "compute", "invoke_scopes": []any{map[string]any{"resource_kind": "profiles", "actions": []string{"invoke", "read"}}}, "lookup_scopes": []any{map[string]any{"resource_kind": "profiles", "actions": []string{"read"}}}}}
		}
		manifest := &solutionhost.ModuleManifest{Schema: solutionhost.SchemaModuleManifestV1, Binding: "acme.test." + prefix, Alias: prefix, Kind: solutionhost.KindModule, Generation: 1, Host: solutionhost.HostTarget{Coordinate: "host.test", Domain: "acme"}, Release: solutionhost.Release{Publisher: "acme", Name: prefix, Version: "1.0.0"}, Workloads: []solutionhost.Workload{{Service: "api", Container: "api"}}, Authority: authority}
		payload, err := solutionhost.SignedPayload(manifest)
		require.NoError(t, err)
		digest, err := manifest.Digest()
		require.NoError(t, err)
		f.records = append(f.records, &business.SolutionHostBindingRecord{BindingID: manifest.Binding, Applied: &business.SolutionHostBindingApplied{SolutionHostBindingGeneration: business.SolutionHostBindingGeneration{Generation: 1, Digest: digest, Document: string(payload)}}})
		f.deliveries[manifest.Binding] = &business.SolutionDeliveryRecord{Binding: manifest.Binding, Generation: 1, Payload: payload, Envelope: deliverytest.Envelope(t, payload, "fixture")}
		f.live[business.ModulePrincipalID(prefix)] = &business.LiveModuleAuthority{RegistryBindingID: manifest.Binding, InstallationID: uuid.NewString(), InstallationRevision: 1, ProducerEpoch: 1, AppliedManifestGeneration: 1, EnvelopeRevision: func() *uint64 { v := uint64(1); return &v }()}
		principals[prefix] = map[string]any{"grant": grant}
	}
	raw, err := json.Marshal(map[string]any{"revision": 1, "principals": principals})
	require.NoError(t, err)
	f.envelope = &business.ModuleEnvelopeRecord{Revision: 1, Document: raw}
	reconciler, err := business.NewSolutionHostBindingReconciler(svc, business.SolutionHostBindingReconcilerConfig{Source: business.NewDeliveredSolutionHostBindings(svc), Verifier: verifier, Coordinate: "host.test", Envelopes: svc})
	require.NoError(t, err)
	require.NoError(t, reconciler.ModulePrincipals().RunOnce(context.Background()))
	for id, grant := range svc.ModulePrincipals() {
		f.live[id].Scope = business.FullInstallationScope(grant)
	}
	facts := &business.WorkContextAuthorityFacts{OrganizationRevision: 7, PrincipalRevision: 3}
	server := newCoreTestServer(t, f, newSealAnswersFake(facts.EffectiveRevision()))
	server.boundaries = &solutionBoundaryAuthorityFake{}
	server.seals = installedReadSeals{server.seals.(*sealAnswersFake), f}
	workContextSingleton = server
	SetInternalToken("installed-read-test")
	return f, server
}

func TestInstalledCollectionReaderAdmitsAuthenticChildAndUsesBothReadOracles(t *testing.T) {
	f, server := installedReadFixture(t)
	caller := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
	request := &gen.StartTaskWorkContextRequest{OrgId: readOrg, TaskId: uuid.NewString(), Audience: "reader", AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "documents", Actions: []string{"read"}}, {ResourceKind: "runs", Actions: []string{"execute"}}, {ResourceKind: "profiles", Actions: []string{"invoke", "read"}, ResourceIds: []string{"standard"}}}}
	parent, err := server.StartTask(caller, request)
	require.NoError(t, err, "a collection reader must be admitted without a kind-wide role")
	_, err = server.verifyFor(context.Background(), parent.Token, "reader")
	require.NoError(t, err)
	moduleToken, _, err := server.StartModuleTask(context.Background(), business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID("reader"), Tenant: readOrg, InstallationID: f.live[business.ModulePrincipalID("reader")].InstallationID})
	require.NoError(t, err)
	exchangeContext := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "installed-read-test", workContextHeader, moduleToken))
	child, err := (&ModuleCapabilitiesServer{}).ExchangeDelegatedOperationAudience(exchangeContext, &gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "evidence", ParentWorkContextToken: parent.Token})
	require.NoError(t, err)
	claims, err := server.verifyFor(context.Background(), child.Token, "documents")
	require.NoError(t, err)
	require.Equal(t, parent.SessionId, claims.Context().GetParentSessionId())
	require.Equal(t, parent.TaskId, child.TaskId)
	require.Equal(t, business.ModulePrincipalID("reader"), child.CurrentActorPrincipalId)
	require.LessOrEqual(t, child.ExpiresAt.AsTime().Unix(), parent.ExpiresAt.AsTime().Unix())
	require.Len(t, claims.EffectiveScopes(), 1)
	require.Equal(t, "documents", claims.EffectiveScopes()[0].ResourceKind)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "installed-read-test", workContextHeader, child.Token))
	oracle := &ModuleCapabilitiesServer{}
	page, err := oracle.ListReadableSourceCollections(ctx, &gen.ListReadableSourceCollectionsRequest{PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.Collections, 2)
	record := &gen.CheckWorkContextRecordAccessRequest{ResourceType: "documents", ResourceId: "boundary-a", Action: "read"}
	exact, err := oracle.CheckWorkContextRecordAccess(ctx, record)
	require.NoError(t, err)
	require.True(t, exact.Allowed)
	f.grants[readOwner] = f.grants[readOwner][1:]
	page, err = oracle.ListReadableSourceCollections(ctx, &gen.ListReadableSourceCollectionsRequest{PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.Collections, 1)
	exact, err = oracle.CheckWorkContextRecordAccess(ctx, record)
	require.NoError(t, err)
	require.False(t, exact.Allowed)
	f.grants[readOwner] = nil
	_, err = oracle.ListReadableSourceCollections(ctx, &gen.ListReadableSourceCollectionsRequest{PageSize: 10})
	require.Error(t, err)
	_, err = oracle.CheckWorkContextRecordAccess(ctx, record)
	require.Error(t, err)
}

func TestInstalledUseKeepsOrdinaryAdmissionAndPartitionsEveryChild(t *testing.T) {
	f, server := installedReadFixture(t)
	caller := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
	request := &gen.StartTaskWorkContextRequest{OrgId: readOrg, TaskId: uuid.NewString(), Audience: "reader", AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "documents", Actions: []string{"read"}}, {ResourceKind: "profiles", Actions: []string{"invoke", "read"}, ResourceIds: []string{"standard"}}}}
	f.broad = true
	ordinary, err := server.StartTask(caller, request)
	require.NoError(t, err)
	ordinaryClaims, err := server.verifyFor(context.Background(), ordinary.Token, "reader")
	require.NoError(t, err)
	require.Empty(t, ordinaryClaims.Context().GetOperationBinding().GetBindingId(), "ordinary role-authorized admission keeps its existing unbound path")
	f.broad = false
	parent, err := server.StartTask(caller, request)
	require.NoError(t, err)
	verified, err := server.verifyFor(context.Background(), parent.Token, "reader")
	require.NoError(t, err)
	module := business.ModuleCaller{PrincipalID: business.ModulePrincipalID("reader"), BoundOrg: readOrg}
	for _, lookup := range []bool{false, true} {
		child, err := server.exchangeInstalledUse(context.Background(), verified, module, delegatedAudienceRequest{kind: "operation", bindingID: "execution", lookup: lookup}, time.Minute)
		require.NoError(t, err)
		checked, err := server.verifyFor(context.Background(), child.Token, "compute")
		require.NoError(t, err)
		require.Len(t, checked.EffectiveScopes(), 1)
		require.Equal(t, []string{"standard"}, checked.EffectiveScopes()[0].ResourceIds)
		if lookup {
			require.Equal(t, []string{"read"}, checked.EffectiveScopes()[0].Actions)
		}
		_, err = server.verifyFor(context.Background(), child.Token, "documents")
		require.Error(t, err)
		_, err = server.exchangeInstalledUse(context.Background(), checked, module, delegatedAudienceRequest{kind: "operation", bindingID: "evidence"}, time.Minute)
		require.Error(t, err)
	}
	for _, bad := range []business.ModuleCaller{{PrincipalID: business.ModulePrincipalID("compute"), BoundOrg: readOrg}, {PrincipalID: module.PrincipalID, BoundOrg: uuid.NewString()}} {
		_, err = server.exchangeInstalledUse(context.Background(), verified, bad, delegatedAudienceRequest{kind: "operation", bindingID: "evidence"}, time.Minute)
		require.Error(t, err)
	}
	_, _, err = server.verifyParent(caller, readOrg, readOwner, parent.Token)
	require.Error(t, err, "generic owner transforms cannot shed the captured binding")
}

func TestInstalledUseDistinguishesAuthorityOutageFromRefusal(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code codes.Code
	}{
		{"inactive", business.ErrModuleInstallationInactive, codes.PermissionDenied},
		{"registry stale", business.ErrModuleRegistryStale, codes.Unavailable},
		{"store unavailable", errors.New("database unavailable"), codes.Unavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, server := installedReadFixture(t)
			caller := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
			request := &gen.StartTaskWorkContextRequest{OrgId: readOrg, TaskId: uuid.NewString(), Audience: "reader", AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "documents", Actions: []string{"read"}}}}
			parent, err := server.StartTask(caller, request)
			require.NoError(t, err)
			verified, err := server.verifyFor(context.Background(), parent.Token, "reader")
			require.NoError(t, err)
			f.liveErr = test.err
			internal := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "installed-read-test"))
			binding := verified.Context().GetOperationBinding().GetBindingId()
			_, err = server.CheckAuthorizationRevision(internal, &gen.CheckAuthorizationRevisionRequest{OrgId: readOrg, OwnerPrincipalId: readOwner, AuthorizationRevision: verified.Context().AuthorizationRevision, OperationBindingId: &binding, Subjects: []*gen.WorkContextRevisionSubject{{PrincipalId: readOwner, Scopes: request.AuthorityScopes}}})
			require.Equal(t, test.code, status.Code(err))

			_, err = server.StartTask(caller, request)
			require.Equal(t, test.code, status.Code(err))
			err = server.requireInstalledUseAuthority(caller, readOrg, verified.Context())
			require.Equal(t, test.code, status.Code(err))
			_, err = server.exchangeInstalledUse(caller, verified, business.ModuleCaller{PrincipalID: business.ModulePrincipalID("reader"), BoundOrg: readOrg}, delegatedAudienceRequest{kind: "operation", bindingID: "evidence"}, time.Minute)
			require.Equal(t, test.code, status.Code(err))
		})
	}
}

func TestOrdinaryModuleHopRevisionDistinguishesAuthorityOutageFromRefusal(t *testing.T) {
	for _, test := range []struct {
		name    string
		binding string
		liveErr error
		code    codes.Code
	}{
		{"current", "evidence", nil, codes.OK},
		{"stale binding", "removed-binding", nil, codes.PermissionDenied},
		{"inactive installation", "evidence", business.ErrModuleInstallationInactive, codes.PermissionDenied},
		{"live read unavailable", "evidence", status.Error(codes.Unavailable, "live read unavailable"), codes.Unavailable},
		{"registry unavailable", "evidence", business.ErrModuleRegistryStale, codes.Unavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, server := installedReadFixture(t)
			consumer := &workContextConsumerAuthorityFake{}
			server.consumer = consumer
			f.liveErr = test.liveErr
			internal := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "installed-read-test"))
			scopes := []*gen.WorkContextScope{{ResourceKind: "documents", Actions: []string{"read"}}}
			_, err := server.CheckAuthorizationRevision(internal, &gen.CheckAuthorizationRevisionRequest{
				OrgId: readOrg, OwnerPrincipalId: readOwner, AuthorizationRevision: 29, OperationBindingId: &test.binding,
				Subjects: []*gen.WorkContextRevisionSubject{{PrincipalId: readOwner, Scopes: scopes}, {PrincipalId: business.ModulePrincipalID("reader"), Scopes: scopes}},
			})
			require.Equal(t, test.code, status.Code(err), "%v", err)
			if test.code == codes.OK {
				require.Len(t, consumer.revisionSubjects, 1, "only the human is checked through the row-backed authority store")
				require.Equal(t, readOwner, consumer.revisionSubjects[0].PrincipalID)
			}
		})
	}
}
