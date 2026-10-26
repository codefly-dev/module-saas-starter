//go:build !pure

package infra_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"accounts/pkg/adapters"
	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

// Owner integration evidence, not native workload/composition qualification:
// the harness controls delivery TokenReview, records policy-log writes, and
// supplies test signing/execution bindings. The module credential is created
// directly by StartModuleTask. Registry, applied generations, installations,
// human grants and authority reads use migrated PostgreSQL and owner adapters.
func TestInstalledContentReadPostgresRetainedBinding(t *testing.T) {
	s := newRegistryScenario(t, "contentreader")
	target := newRegistryBinding(t, "contenttarget")
	svc := s.delivery.service
	manifests := map[string]*solutionhost.ModuleManifest{}
	for _, b := range []*registryBinding{s.primary, target} {
		m := productionPathManifest(t, b.solutionID, 1)
		m.Kind = solutionhost.KindModule
		m.Authority = &solutionhost.Authority{Principal: b.solutionID}
		if b == s.primary {
			m.Authority.Bindings = []solutionhost.AuthorityBinding{{ID: "evidence", Audience: target.solutionID, Operations: map[string][]string{"invoke": {"documents:read"}, "lookup": {"documents:read"}}}}
		} else {
			m.Authority.ResourceKinds = []string{"documents"}
		}
		manifests[b.solutionID] = m
	}
	read := []business.ModuleOperationScope{{ResourceKind: "documents", Actions: []string{"read"}}}
	grants := map[string]business.ModuleEnvelopePrincipal{
		s.prefix:          {Grant: business.ModulePrincipalGrant{Tenant: s.tenant, OperationAudiences: map[string]business.ModuleOperationAudience{"evidence": {Audience: target.solutionID, InvokeScopes: read, LookupScopes: read, DelegationMode: business.DelegationModeContentRead}}}},
		target.solutionID: {Grant: business.ModulePrincipalGrant{Tenant: s.tenant, Resources: []string{"documents"}}},
	}
	put := func() {
		t.Helper()
		rev := s.currentRevision(t)
		wire := map[string]any{}
		for prefix, entry := range grants {
			wire[prefix] = map[string]any{"grant": map[string]any{"tenant": entry.Grant.Tenant, "resources": entry.Grant.Resources, "operation_audiences": entry.Grant.OperationAudiences}}
		}
		raw, err := json.Marshal(map[string]any{"revision": rev + 1, "principals": wire})
		require.NoError(t, err)
		_, err = svc.PutModuleEnvelope(testCtx, s.admin, rev, raw)
		require.NoError(t, err)
		require.NotErrorIs(t, s.passReasons(t), business.ErrModulePrincipalInputsUnreadable)
	}
	// First establish host ceiling, then deliver both signed owner declarations.
	put()
	for _, b := range []*registryBinding{s.primary, target} {
		requireDelivered(t, s.delivery.deliver(t, manifests[b.solutionID]))
	}
	s.pass(t)
	originInstall := s.install(t, s.tenant)
	targetScenario := *s
	targetScenario.prefix, targetScenario.primary = target.solutionID, target
	targetInstall := targetScenario.install(t, s.tenant)
	svc.SetWorkContextSealStore(testStore)
	svc.SetExecutionBinding(executiontest.New())
	owner := seedUser(t)
	seedHumanPrincipal(t, owner, "Collection reader")
	require.NoError(t, testStore.As(business.Identity{OrgID: s.tenant}).AddOrgMember(testCtx, owner, "member"))
	source := seedDatasourceSource(t, s.tenant)
	var path string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx, `SELECT n.scope_path::text FROM datasource_sources s JOIN scope_nodes n ON n.id=s.boundary_node_id WHERE s.id=$1`, source).Scan(&path)
	}))
	registerNode(t, s.tenant, path+".record", "record", "documents", "known-document")
	role := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, s.tenant, func(ctx context.Context) error {
		if err := testStore.CreateRole(ctx, &gen.Role{Id: role, OrgId: s.tenant, Name: "Collection reader " + role, Permissions: []*gen.Permission{{Resource: "documents", Action: "read"}}}); err != nil {
			return err
		}
		return testStore.GrantScope(ctx, &gen.ScopeGrant{Id: business.NewIDString(), OrgId: s.tenant, SubjectId: owner, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: path, RoleId: role})
	}))
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	previous := *adapters.WorkContextSingleton()
	t.Cleanup(func() {
		*adapters.WorkContextSingleton() = previous
		adapters.WithService(nil)
		adapters.SetInternalToken("")
	})
	issuer := adapters.WorkContextSingleton()
	issuer.Configure(adapters.WorkContextAuthorityConfiguration{Issuer: "accounts.test", KeyID: "content-test", PrivateKey: key, Authority: testStore, Seals: svc})
	adapters.WithService(svc)
	adapters.SetInternalToken(delegationExchangeInternalToken)
	caller := auth.WithVerifiedDatabaseIdentity(testCtx, owner, s.tenant)
	root, err := issuer.StartTask(caller, &gen.StartTaskWorkContextRequest{OrgId: s.tenant, TaskId: business.NewIDString(), Audience: s.prefix, AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "documents", Actions: []string{"read"}}}})
	require.NoError(t, err)
	moduleToken, _, err := issuer.StartModuleTask(testCtx, business.ModuleWorkContextAuthority{PrincipalID: s.principalID(), Tenant: s.tenant, InstallationID: originInstall.Id})
	require.NoError(t, err)
	moduleCtx := metadata.NewIncomingContext(testCtx, metadata.Pairs("x-codefly-internal-token", delegationExchangeInternalToken, "x-codefly-work-context", moduleToken))
	exchange := func(lookup bool) (*gen.IssuedWorkContext, error) {
		return adapters.ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(moduleCtx, &gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "evidence", ParentWorkContextToken: root.Token, Lookup: lookup})
	}
	child, err := exchange(false)
	require.NoError(t, err)
	require.Equal(t, s.principalID(), child.CurrentActorPrincipalId)
	claims, err := workcontext.Decode(child.Token)
	require.NoError(t, err)
	require.True(t, business.NamesInstalledContentRead(claims.Context().GetOperationBinding().GetBindingId()))
	lookup, err := exchange(true)
	require.NoError(t, err)
	require.Equal(t, s.principalID(), lookup.CurrentActorPrincipalId)
	// The child is addressed to the target module, which is the one that asks:
	// with its own identity, minted against its current installation, in the
	// header, and the child it holds in the request.
	// While the target is uninstalled it has no identity to mint, so it asks
	// with none and is refused for that.
	asTarget := func() context.Context {
		t.Helper()
		pairs := []string{"x-codefly-internal-token", delegationExchangeInternalToken}
		if credential, _, err := issuer.StartModuleTask(testCtx, business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID(target.solutionID), Tenant: s.tenant, InstallationID: targetInstall.Id}); err == nil {
			pairs = append(pairs, "x-codefly-work-context", credential)
		}
		return metadata.NewIncomingContext(testCtx, metadata.Pairs(pairs...))
	}
	check := func(viewer string) (*gen.CheckWorkContextRecordAccessResponse, error) {
		t.Helper()
		return adapters.ModuleCapabilitiesSingleton().CheckWorkContextRecordAccess(asTarget(),
			&gen.CheckWorkContextRecordAccessRequest{ResourceType: "documents", ResourceId: "known-document", Action: "read", ViewerWorkContextToken: viewer})
	}
	decision, err := check(child.Token)
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	page, err := adapters.ModuleCapabilitiesSingleton().ListReadableSourceCollections(asTarget(), &gen.ListReadableSourceCollectionsRequest{PageSize: 10, ViewerWorkContextToken: child.Token})
	require.NoError(t, err)
	require.Len(t, page.Collections, 1)
	// An actual monotonic host update removes then identically restores the read.
	original := grants[s.prefix]
	withdrawn := original
	withdrawn.Grant.OperationAudiences = nil
	grants[s.prefix] = withdrawn
	put()
	_, err = exchange(false)
	require.Error(t, err)
	_, err = check(child.Token)
	require.Error(t, err)
	grants[s.prefix] = original
	put()
	_, err = check(child.Token)
	require.Error(t, err, "identical policy cannot revive old binding lifetime")
	// Existing installation consent remains authoritative after envelope changes.
	// A new child is allowed only if that same consent still covers the binding.
	_, _, err = svc.InstalledContentReadBinding(testCtx, s.caller(s.tenant), "evidence")
	require.NoError(t, err)
	// Each case starts with a fresh signed child, then moves an owner fact
	// through its actual writer. The retained token must fail verification at
	// the served record check, not merely differ in a decoded binding string.
	freshRead := func() string {
		t.Helper()
		root, err = issuer.StartTask(caller, &gen.StartTaskWorkContextRequest{OrgId: s.tenant, TaskId: business.NewIDString(), Audience: s.prefix, AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "documents", Actions: []string{"read"}}}})
		require.NoError(t, err)
		moduleToken, _, err = issuer.StartModuleTask(testCtx, business.ModuleWorkContextAuthority{PrincipalID: s.principalID(), Tenant: s.tenant, InstallationID: originInstall.Id})
		require.NoError(t, err)
		moduleCtx = metadata.NewIncomingContext(testCtx, metadata.Pairs("x-codefly-internal-token", delegationExchangeInternalToken, "x-codefly-work-context", moduleToken))
		next, err := exchange(false)
		require.NoError(t, err)
		allowed, err := check(next.Token)
		require.NoError(t, err)
		require.True(t, allowed.Allowed)
		return next.Token
	}
	t.Run("content mode removed", func(t *testing.T) {
		token := freshRead()
		heldClaims, err := workcontext.Decode(token)
		require.NoError(t, err)
		bindingID := heldClaims.Context().GetOperationBinding().GetBindingId()
		ask := func() (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
			t.Helper()
			credential, _, err := issuer.StartModuleTask(testCtx, business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID(target.solutionID), Tenant: s.tenant, InstallationID: targetInstall.Id})
			require.NoError(t, err)
			callee := metadata.NewIncomingContext(testCtx, metadata.Pairs("x-codefly-internal-token", delegationExchangeInternalToken, "x-codefly-work-context", credential))
			return adapters.ModuleCapabilitiesSingleton().ResolveWorkContextAuthority(callee, &gen.ModuleResolveWorkContextAuthorityRequest{
				WorkContext: token,
				Question:    &gen.ModuleResolveWorkContextAuthorityRequest_OperationBinding{OperationBinding: &gen.WorkContextOperationBindingQuestion{BindingId: bindingID}},
			})
		}
		answer, err := ask()
		require.NoError(t, err)
		require.Equal(t, gen.WorkContextOperationBindingState_WORK_CONTEXT_OPERATION_BINDING_STATE_RESOLVED, answer.GetOperationBinding().GetState())

		// The ordinary binding remains installed. Removing its content-read mode
		// is a witnessed restriction, and the callee must receive an explicit
		// absence for the retained child's old binding, not an authority outage.
		marked := grants[s.prefix].Grant.OperationAudiences["evidence"]
		ordinary := marked
		ordinary.DelegationMode = ""
		grants[s.prefix].Grant.OperationAudiences["evidence"] = ordinary
		put()
		defer func() {
			grants[s.prefix].Grant.OperationAudiences["evidence"] = marked
			put()
		}()
		answer, err = ask()
		require.NoError(t, err, "completed revocation must be a decided absence, not Unavailable")
		require.Equal(t, bindingID, answer.GetOperationBinding().GetBindingId())
		require.Equal(t, gen.WorkContextOperationBindingState_WORK_CONTEXT_OPERATION_BINDING_STATE_ABSENT, answer.GetOperationBinding().GetState())
	})
	for _, which := range []string{"target presence", "origin presence", "target installation", "origin installation"} {
		t.Run(which, func(t *testing.T) {
			held := freshRead()
			switch which {
			case "target presence", "origin presence":
				b := target
				if which == "origin presence" {
					b = s.primary
				}
				m := manifests[b.solutionID]
				m.Generation++
				requireDelivered(t, s.delivery.deliver(t, m))
				s.pass(t)
			case "target installation":
				require.NoError(t, svc.UninstallSolution(testCtx, s.admin, s.tenant, targetInstall.Id))
				_, err = check(held)
				require.Error(t, err)
				targetInstall = targetScenario.install(t, s.tenant)
			case "origin installation":
				require.NoError(t, svc.UninstallSolution(testCtx, s.admin, s.tenant, originInstall.Id))
				_, err = check(held)
				require.Error(t, err)
				originInstall = s.install(t, s.tenant)
			}
			_, err = check(held)
			require.Error(t, err, "changed owner lifetime must reject retained signed child")
		})
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, s.tenant, func(ctx context.Context) error {
		return testStore.RevokeScope(ctx, s.tenant, owner, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, path, role)
	}))
	_, err = issuer.StartTask(caller, &gen.StartTaskWorkContextRequest{OrgId: s.tenant, TaskId: business.NewIDString(), Audience: s.prefix, AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "documents", Actions: []string{"read"}}}})
	require.Error(t, err, "collection-only standing is not a visibility grant")
}
