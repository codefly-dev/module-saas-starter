//go:build !pure

package infra_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"accounts/pkg/adapters"
	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// A module's delegated exchanges take a parent whose owner the host resolves
// current authority for: an organization membership and an authorization
// revision, both rows a declared module principal never has. A module
// operation context minted through StartModuleOperationTask, presented as the
// parent to either exchange by the module it is addressed to, is therefore
// refused before any binding is resolved. Run against the migrated store, not a
// fake, because the refusal is the store's.
func TestADelegatedExchangeRefusesAModuleOperationContextAsItsParent(t *testing.T) {
	s := newRegistryScenario(t, "opsource")
	target := newRegistryBinding(t, "optarget")
	svc := s.delivery.service
	manifests := map[string]*solutionhost.ModuleManifest{}
	for _, b := range []*registryBinding{s.primary, target} {
		m := productionPathManifest(t, b.solutionID, 1)
		m.Kind = solutionhost.KindModule
		m.Authority = &solutionhost.Authority{Principal: b.solutionID, ResourceKinds: []string{"documents"}}
		if b == s.primary {
			m.Authority.Bindings = []solutionhost.AuthorityBinding{{ID: "publish", Audience: target.solutionID, Operations: map[string][]string{"invoke": {"documents:read"}, "headless": {"documents:read"}}}}
		}
		manifests[b.solutionID] = m
	}
	read := []business.ModuleOperationScope{{ResourceKind: "documents", Actions: []string{"read"}}}
	// The operation context's audience is the target; its scopes are read.
	grants := map[string]business.ModuleEnvelopePrincipal{
		s.prefix:          {Grant: business.ModulePrincipalGrant{Tenant: s.tenant, Resources: []string{"documents"}, OperationAudiences: map[string]business.ModuleOperationAudience{"publish": {Audience: target.solutionID, InvokeScopes: read, HeadlessScopes: read}}}},
		target.solutionID: {Grant: business.ModulePrincipalGrant{Tenant: s.tenant, Resources: []string{"documents"}}},
	}
	rev := s.currentRevision(t)
	wire := map[string]any{}
	for prefix, entry := range grants {
		wire[prefix] = map[string]any{"grant": map[string]any{"tenant": entry.Grant.Tenant, "resources": entry.Grant.Resources, "operation_audiences": entry.Grant.OperationAudiences}}
	}
	raw, err := json.Marshal(map[string]any{"revision": rev + 1, "principals": wire})
	require.NoError(t, err)
	_, err = svc.PutModuleEnvelope(testCtx, s.admin, rev, raw)
	require.NoError(t, err)
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
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	previous := *adapters.WorkContextSingleton()
	t.Cleanup(func() {
		*adapters.WorkContextSingleton() = previous
		adapters.WithService(nil)
		adapters.SetInternalToken("")
	})
	issuer := adapters.WorkContextSingleton()
	issuer.Configure(adapters.WorkContextAuthorityConfiguration{Issuer: "accounts.test", KeyID: "service-parent-test", PrivateKey: key, Authority: testStore, Seals: svc})
	adapters.WithService(svc)
	adapters.SetInternalToken(delegationExchangeInternalToken)

	parent, _, err := issuer.StartModuleOperationTask(testCtx, business.ModuleOperationContextAuthority{
		ModuleWorkContextAuthority: business.ModuleWorkContextAuthority{PrincipalID: s.principalID(), Tenant: s.tenant, InstallationID: originInstall.Id},
		BindingID:                  "publish", ModulePrefix: s.prefix, Audience: target.solutionID, Scopes: read, Revision: 1,
	})
	require.NoError(t, err)
	moduleToken, _, err := issuer.StartModuleTask(testCtx, business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID(target.solutionID), Tenant: s.tenant, InstallationID: targetInstall.Id})
	require.NoError(t, err)
	callee := metadata.NewIncomingContext(testCtx, metadata.Pairs("x-codefly-internal-token", delegationExchangeInternalToken, "x-codefly-work-context", moduleToken))
	refused := func(t *testing.T, out *gen.IssuedWorkContext, err error) {
		t.Helper()
		require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
		require.ErrorContains(t, err, "current organization membership and authorization revision required")
		require.Nil(t, out)
	}
	t.Run("ExchangeDelegatedOperationAudience", func(t *testing.T) {
		out, err := adapters.ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(callee,
			&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "back", ParentWorkContextToken: parent})
		refused(t, out, err)
	})
	t.Run("ExchangeDelegatedReadAudience", func(t *testing.T) {
		out, err := adapters.ModuleCapabilitiesSingleton().ExchangeDelegatedReadAudience(callee,
			&gen.ModuleExchangeDelegatedReadAudienceRequest{BindingId: "back", ParentWorkContextToken: parent})
		refused(t, out, err)
	})
}
