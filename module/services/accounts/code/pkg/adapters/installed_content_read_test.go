package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	corework "github.com/codefly-dev/core/workcontext"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/internal/deliverytest"
	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
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
		if p.ResourceKind == "documents" && (!p.ContentRead && !f.broad && p.ResourceID == "" || len(f.grants[readOwner]) == 0) {
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
			authority.Bindings = []solutionhost.AuthorityBinding{{ID: "evidence", Audience: "documents", Operations: map[string][]string{"invoke": {"documents:read"}, "lookup": {"documents:read"}}}, {ID: "execution", Audience: "compute", Operations: map[string][]string{"invoke": {"profiles:read", "profiles:invoke"}, "lookup": {"profiles:read"}}}}
			grant["operation_audiences"] = map[string]any{"evidence": map[string]any{"audience": "documents", "delegation_mode": "content_read", "lookup_scopes": []any{map[string]any{"resource_kind": "documents", "actions": []string{"read"}, "resource_ids": []string{"boundary-a"}}}, "invoke_scopes": []any{map[string]any{"resource_kind": "documents", "actions": []string{"read"}}}}, "execution": map[string]any{"audience": "compute", "invoke_scopes": []any{map[string]any{"resource_kind": "profiles", "actions": []string{"invoke", "read"}}}, "lookup_scopes": []any{map[string]any{"resource_kind": "profiles", "actions": []string{"read"}}}}}
			addModuleActorBindings(authority, grant)
		}
		manifest := &solutionhost.ModuleManifest{Schema: solutionhost.SchemaModuleManifestV1, Binding: "acme.test." + prefix, Alias: prefix, Kind: solutionhost.KindModule, Generation: 1, Host: solutionhost.HostTarget{Coordinate: "host.test", Domain: "acme"}, Release: solutionhost.Release{Publisher: "acme", Name: prefix, Version: "1.0.0"}, Workloads: []solutionhost.Workload{{Service: "api", Container: "api"}}, Authority: authority}
		payload, err := solutionhost.SignedPayload(manifest)
		require.NoError(t, err)
		digest, err := manifest.Digest()
		require.NoError(t, err)
		f.records = append(f.records, &business.SolutionHostBindingRecord{BindingID: manifest.Binding, Applied: &business.SolutionHostBindingApplied{SolutionHostBindingGeneration: business.SolutionHostBindingGeneration{Generation: 1, Digest: digest, Document: string(payload)}}})
		f.deliveries[manifest.Binding] = &business.SolutionDeliveryRecord{Binding: manifest.Binding, Generation: 1, Payload: payload, Envelope: deliverytest.Envelope(t, payload, "fixture")}
		f.live[business.ModulePrincipalID(prefix)] = &business.LiveModuleAuthority{RegistryBindingID: manifest.Binding, InstallationID: uuid.NewString(), InstallationRevision: 1, ProducerEpoch: 1, EnvelopeRevision: func() *uint64 { v := uint64(1); return &v }()}
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

func (s installedReadSeals) WorkContextOperationBinding(ctx context.Context, org, id string) (business.WorkContextOperationBinding, error) {
	return service.WorkContextOperationBinding(ctx, org, id)
}

type installedReadJournal struct {
	business.ActorChainJournal
	hops []business.ActorChainHopInput
	err  error
}

func (j *installedReadJournal) AppendActorChainHop(_ context.Context, hop business.ActorChainHopInput) (*business.ActorChainHop, error) {
	j.hops = append(j.hops, hop)
	return &business.ActorChainHop{ID: hop.ID}, j.err
}

func (j *installedReadJournal) AnyActorChainHopRevoked(context.Context, string, []string) (bool, error) {
	return false, nil
}

func TestInstalledContentReadJournalsBeforeIssuance(t *testing.T) {
	for _, lookup := range []bool{false, true} {
		for _, failAppend := range []bool{false, true} {
			t.Run(fmt.Sprintf("lookup=%t/append_failure=%t", lookup, failAppend), func(t *testing.T) {
				f, server := installedReadFixture(t)
				journal := &installedReadJournal{}
				if failAppend {
					journal.err = errors.New("journal unavailable")
				}
				server.journal = journal
				caller := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
				parent, err := server.StartTask(caller, &gen.StartTaskWorkContextRequest{OrgId: readOrg, TaskId: uuid.NewString(), Audience: "reader", AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "documents", Actions: []string{"read"}}}})
				require.NoError(t, err)
				moduleToken, _, err := server.StartModuleTask(context.Background(), business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID("reader"), Tenant: readOrg, InstallationID: f.live[business.ModulePrincipalID("reader")].InstallationID})
				require.NoError(t, err)
				ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "installed-read-test", workContextHeader, moduleToken))
				child, err := (&ModuleCapabilitiesServer{}).ExchangeDelegatedOperationAudience(ctx, &gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "evidence", ParentWorkContextToken: parent.Token, Lookup: lookup})
				require.Len(t, journal.hops, 1)
				if failAppend {
					require.Equal(t, codes.Internal, status.Code(err))
					require.Nil(t, child, "an unrecorded actor hop must not release a capability")
					return
				}
				require.NoError(t, err)
				verified, err := corework.Decode(child.Token)
				require.NoError(t, err)
				claims := verified.Context()
				require.Len(t, claims.ActorChain, 1)
				hop := journal.hops[0]
				require.Equal(t, claims.ActorChain[0].DelegationId, hop.ID)
				require.Equal(t, readOwner, hop.OwnerPrincipalID)
				require.Equal(t, business.ModulePrincipalID("reader"), hop.ActorPrincipalID)
				require.Equal(t, business.PrincipalKindService, hop.ActorKind)
				require.Equal(t, readOrg, hop.OrgID)
				require.Equal(t, claims.TaskId, hop.TaskID)
				require.Equal(t, claims.SessionId, hop.SessionID)
				require.Equal(t, claims.AuthorizationRevision, hop.AuthorizationRevision)
				require.Equal(t, actorChainScopes(claims.ActorChain[0].GrantedScopes), hop.GrantedScopes)
			})
		}
	}
}

func TestInstalledContentReadCollectionReader(t *testing.T) {
	f, server := installedReadFixture(t)
	caller := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
	scopes := []*gen.WorkContextScope{{ResourceKind: "documents", Actions: []string{"read"}}, {ResourceKind: "runs", Actions: []string{"execute"}}, {ResourceKind: "profiles", Actions: []string{"invoke", "read"}, ResourceIds: []string{"standard"}}}
	parent, err := server.StartTask(caller, &gen.StartTaskWorkContextRequest{OrgId: readOrg, TaskId: uuid.NewString(), Audience: "reader", AuthorityScopes: scopes})
	require.NoError(t, err)
	moduleToken, _, err := server.StartModuleTask(context.Background(), business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID("reader"), Tenant: readOrg, InstallationID: f.live[business.ModulePrincipalID("reader")].InstallationID})
	require.NoError(t, err)
	exchangeContext := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "installed-read-test", workContextHeader, moduleToken))
	exchange := func() (*gen.IssuedWorkContext, error) {
		return (&ModuleCapabilitiesServer{}).ExchangeDelegatedOperationAudience(exchangeContext, &gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "evidence", ParentWorkContextToken: parent.Token})
	}
	for _, lookup := range []bool{false, true} {
		model, err := (&ModuleCapabilitiesServer{}).ExchangeDelegatedOperationAudience(exchangeContext, &gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "execution", ParentWorkContextToken: parent.Token, Lookup: lookup})
		require.NoError(t, err)
		modelClaims, err := corework.Decode(model.Token)
		require.NoError(t, err)
		require.Empty(t, modelClaims.Context().ActorChain, "ordinary Model exchange must preserve its existing human shape")
		require.Equal(t, []string{"standard"}, modelClaims.Context().AuthorityScopes[0].ResourceIds)
	}
	lookup, err := (&ModuleCapabilitiesServer{}).ExchangeDelegatedOperationAudience(exchangeContext, &gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "evidence", ParentWorkContextToken: parent.Token, Lookup: true})
	require.NoError(t, err)
	lookupClaims, err := corework.Decode(lookup.Token)
	require.NoError(t, err)
	require.Equal(t, []string{"boundary-a"}, lookupClaims.Context().ActorChain[0].GrantedScopes[0].ResourceIds)
	child, err := exchange()
	require.NoError(t, err)
	require.Equal(t, business.ModulePrincipalID("reader"), child.CurrentActorPrincipalId)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "installed-read-test", workContextHeader, child.Token))
	oracle := &ModuleCapabilitiesServer{}
	page, err := oracle.ListReadableSourceCollections(ctx, &gen.ListReadableSourceCollectionsRequest{PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.Collections, 2)
	record := &gen.CheckWorkContextRecordAccessRequest{ResourceType: "documents", ResourceId: "boundary-a", Action: "read"}
	exact, err := oracle.CheckWorkContextRecordAccess(ctx, record)
	require.NoError(t, err)
	require.True(t, exact.Allowed)
	// Removing only this collection hides its known record while another remains.
	f.grants[readOwner] = f.grants[readOwner][1:]
	exact, err = oracle.CheckWorkContextRecordAccess(ctx, record)
	require.NoError(t, err)
	require.False(t, exact.Allowed)
	page, err = oracle.ListReadableSourceCollections(ctx, &gen.ListReadableSourceCollectionsRequest{PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.Collections, 1)
	// Owner transformation cannot discard installed provenance.
	_, err = server.ExchangeAudience(caller, &gen.ExchangeWorkContextAudienceRequest{OrgId: readOrg, ParentWorkContextToken: child.Token, Audience: "reader", AttenuatedScopes: scopes})
	require.ErrorContains(t, err, "cannot be transformed")
	_, err = server.StartRootSession(caller, &gen.StartRootSessionWorkContextRequest{SessionId: uuid.NewString(), OrgId: readOrg, ParentWorkContextToken: child.Token, Audience: "reader"})
	require.ErrorContains(t, err, "cannot be transformed")
	_, err = server.StartChildSession(caller, &gen.StartChildSessionWorkContextRequest{OrgId: readOrg, ParentWorkContextToken: child.Token, Audience: "reader", ActorPrincipalId: uuid.NewString(), GrantedScopes: scopes})
	require.ErrorContains(t, err, "cannot be transformed")
	actorCaller := stampVerifiedIdentity(context.Background(), business.ModulePrincipalID("reader"), readOrg, auth.Assurance{})
	_, err = server.RenewWorkContext(actorCaller, &gen.RenewWorkContextRequest{OrgId: readOrg, ParentWorkContextToken: child.Token})
	require.ErrorContains(t, err, "cannot be renewed")
	// Current monotonic envelope facts revoke old children permanently.
	registry := service.ModulePrincipals()
	oldGrant := registry[business.ModulePrincipalID("reader")]
	withdrawn := oldGrant
	withdrawn.OperationAudiences = nil
	registry[business.ModulePrincipalID("reader")] = withdrawn
	f.envelope.Revision = 2
	for _, live := range f.live {
		v := uint64(2)
		live.EnvelopeRevision = &v
	}
	service.InstallDerivedModulePrincipals(registry, 2, nil)
	_, err = exchange()
	require.Error(t, err)
	_, err = oracle.CheckWorkContextRecordAccess(ctx, record)
	require.Error(t, err)
	registry[business.ModulePrincipalID("reader")] = oldGrant
	f.envelope.Revision = 3
	for _, live := range f.live {
		v := uint64(3)
		live.EnvelopeRevision = &v
	}
	service.InstallDerivedModulePrincipals(registry, 3, nil)
	_, err = oracle.CheckWorkContextRecordAccess(ctx, record)
	require.Error(t, err)
	_, err = exchange()
	require.NoError(t, err, "ordinary root permits a newly authorized child")
	f.grants[readOwner] = nil
	_, err = exchange()
	require.Error(t, err)
}

func TestInstalledContentReadRefusesExistingActors(t *testing.T) {
	f, server := installedReadFixture(t)
	server.authority = &readExchangeAuthority{facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 7, PrincipalRevision: 3}}
	server.journal = &readExchangeJournal{}
	scopes := []*basev0.WorkScopeV1{{ResourceKind: "documents", Actions: []string{"read"}}}
	token, _ := mintTestCapability(t, server, testCapability{Audience: "reader", Tenant: readOrg, Owner: readOwner, Scopes: scopes, Actors: []delegationHop{agentHop("actor-1", "hop-1", scopes...)}})
	moduleToken, _, err := server.StartModuleTask(context.Background(), business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID("reader"), Tenant: readOrg, InstallationID: f.live[business.ModulePrincipalID("reader")].InstallationID})
	require.NoError(t, err)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "installed-read-test", workContextHeader, moduleToken))
	result, err := (&ModuleCapabilitiesServer{}).ExchangeDelegatedOperationAudience(ctx, &gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "evidence", ParentWorkContextToken: token})
	require.Error(t, err, "a marked binding cannot issue an ordinary remint for an existing actor")
	require.Nil(t, result)
}

func TestInstalledContentReadNeverFallsBackWhenTargetContentDisappears(t *testing.T) {
	for _, flatRole := range []bool{false, true} {
		t.Run(fmt.Sprint(flatRole), func(t *testing.T) {
			f, server := installedReadFixture(t)
			f.broad = flatRole
			caller := stampVerifiedIdentity(context.Background(), readOwner, readOrg, auth.Assurance{})
			root, err := server.StartTask(caller, &gen.StartTaskWorkContextRequest{OrgId: readOrg, TaskId: uuid.NewString(), Audience: "reader", AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "documents", Actions: []string{"read"}}}})
			require.NoError(t, err)
			moduleToken, _, err := server.StartModuleTask(context.Background(), business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID("reader"), Tenant: readOrg, InstallationID: f.live[business.ModulePrincipalID("reader")].InstallationID})
			require.NoError(t, err)
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "installed-read-test", workContextHeader, moduleToken))
			registry := service.ModulePrincipals()
			target := registry[business.ModulePrincipalID("documents")]
			target.Resources = nil
			registry[business.ModulePrincipalID("documents")] = target
			service.InstallDerivedModulePrincipals(registry, 1, nil)
			for _, lookup := range []bool{false, true} {
				out, err := (&ModuleCapabilitiesServer{}).ExchangeDelegatedOperationAudience(ctx, &gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "evidence", ParentWorkContextToken: root.Token, Lookup: lookup})
				require.Nil(t, out)
				require.Equal(t, codes.PermissionDenied, status.Code(err))
			}
		})
	}
}

func TestInstalledContentReadLifetimeUsesBothOwnerFacts(t *testing.T) {
	for _, dimension := range []string{"origin generation", "target generation", "origin installation", "target installation", "origin revision", "target revision", "origin epoch", "target epoch", "outage"} {
		t.Run(dimension, func(t *testing.T) {
			f, _ := installedReadFixture(t)
			caller := business.ModuleCaller{PrincipalID: business.ModulePrincipalID("reader"), BoundOrg: readOrg}
			held, _, err := service.InstalledContentReadBinding(context.Background(), caller, "evidence")
			require.NoError(t, err)
			prefix := "reader"
			if strings.HasPrefix(dimension, "target") {
				prefix = "documents"
			}
			id := business.ModulePrincipalID(prefix)
			live := f.live[id]
			switch {
			case strings.HasSuffix(dimension, "generation"):
				registry := service.ModulePrincipals()
				grant := registry[id]
				grant.PresenceGeneration++
				registry[id] = grant
				service.InstallDerivedModulePrincipals(registry, 1, nil)
			case strings.HasSuffix(dimension, "installation"):
				live.InstallationID = uuid.NewString()
			case strings.HasSuffix(dimension, "revision"):
				live.InstallationRevision++
			case strings.HasSuffix(dimension, "epoch"):
				live.ProducerEpoch++
			case dimension == "outage":
				f.liveErr = errors.New("database unavailable")
			}
			_, err = service.WorkContextOperationBinding(context.Background(), readOrg, held.ID)
			require.Error(t, err)
		})
	}
}

func TestInstalledContentReadRefusesNonhumanRoot(t *testing.T) {
	f, server := installedReadFixture(t)
	server.authority = &readExchangeAuthority{facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 7, PrincipalRevision: 3}}
	scopes := []*basev0.WorkScopeV1{{ResourceKind: "documents", Actions: []string{"read"}}}
	token, _ := mintTestCapability(t, server, testCapability{Audience: "reader", Tenant: readOrg, Owner: readOwner, OwnerKind: business.PrincipalKindService, Scopes: scopes})
	moduleToken, _, err := server.StartModuleTask(context.Background(), business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID("reader"), Tenant: readOrg, InstallationID: f.live[business.ModulePrincipalID("reader")].InstallationID})
	require.NoError(t, err)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "installed-read-test", workContextHeader, moduleToken))
	for _, lookup := range []bool{false, true} {
		result, err := (&ModuleCapabilitiesServer{}).ExchangeDelegatedOperationAudience(ctx, &gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "evidence", ParentWorkContextToken: token, Lookup: lookup})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Nil(t, result)
	}
}
