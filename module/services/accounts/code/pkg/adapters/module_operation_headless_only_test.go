package adapters

import (
	"context"
	"errors"
	"strings"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/business/moduleregistrytest"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A HEADLESS-ONLY binding (#1156) through the real RPCs: the headless mint
// seals exactly its headless scopes, and every person-delegated exchange — a
// parent token, a delegation-bearing parent, a delegation reference — refuses it
// by name. It was granted no invoke, so it never acts on a person's behalf.

const headlessOnlyPrincipals = `{"documents":{"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},"tenant":"` + moduleWorkContextTenant + `","operation_audiences":{` +
	`"embed":{"audience":"modelservice",` +
	`"headless_scopes":[{"resource_kind":"modelservice.profiles","actions":["invoke"],"resource_ids":["profile-digest"]}]}}}}`

func TestMintModuleOperationContextSealsAHeadlessOnlyBinding(t *testing.T) {
	_, audit := installModuleOperationContextService(t, headlessOnlyPrincipals)
	installModuleWorkContextAuthority(t)

	resp, err := mintModuleOperationContext("documents", moduleToken(t, "documents"), "embed")

	require.NoError(t, err)
	require.Equal(t, "modelservice", resp.GetAudience())
	signed := verifyOperationContext(t, resp.GetToken())
	requireScopes(t, []*basev0.WorkScopeV1{{ResourceKind: "modelservice.profiles", Actions: []string{"invoke"}, ResourceIds: []string{"profile-digest"}}},
		signed.GetAuthorityScopes())
	require.Equal(t, business.ModulePrincipalID("documents"), signed.GetOwnerPrincipalId(), "owned by the module itself, never a person")
	require.Empty(t, signed.GetActorChain())
	require.Len(t, audit.of(business.EventModuleOperationContextMint), 1)
}

func requireHeadlessOnlyRefusal(t *testing.T, err error, binding string) {
	t.Helper()
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		require.Equal(t, connect.CodePermissionDenied, connectErr.Code(), "%v", err)
	} else {
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	}
	require.True(t, strings.Contains(err.Error(), `"`+binding+`" is headless-only`), "the refusal names why: %v", err)
}

func TestAPersonsParentCannotRedeemAHeadlessOnlyBinding(t *testing.T) {
	_, _, client, _ := sourceReadFixture(t)
	registry := installedOperationRegistry(true)
	example := registry[business.ModulePrincipalID("example")]
	example.OperationAudiences["background"] = business.ModuleOperationAudience{
		Audience:       "example-producer",
		HeadlessScopes: []business.ModuleOperationScope{{ResourceKind: "results", Actions: []string{"write"}, ResourceIDs: []string{"result-1"}}},
	}
	service.SetModuleAuthorityReads(currentModuleAuthority{service: service}, nil)
	service.SetModuleCapabilities(nil, nil, registry)
	// The person holds everything the binding's audience could ask for.
	parent, _ := mintTestCapability(t, workContextSingleton, testCapability{
		Audience: "example", Tenant: readOrg, Owner: readOwner,
		Task: "019f6bf7-1111-7111-8111-111111111111", Session: "019f6bf7-2222-7222-8222-222222222222",
		Scopes: []*basev0.WorkScopeV1{{ResourceKind: "results", Actions: []string{"read", "write"}, ResourceIds: []string{"result-1"}}},
	})
	exchange := func(binding string, lookup bool) error {
		req := connect.NewRequest(&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: binding, ParentWorkContextToken: parent, Lookup: lookup})
		req.Header().Set(workContextHeader, mintModuleIdentity(t, business.ModulePrincipalID("example"), readOrg))
		req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
		out, err := client.ExchangeDelegatedOperationAudience(context.Background(), req)
		require.Nil(t, out)
		return err
	}

	require.NoError(t, func() error {
		req := connect.NewRequest(&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "generate", ParentWorkContextToken: parent})
		req.Header().Set(workContextHeader, mintModuleIdentity(t, business.ModulePrincipalID("example"), readOrg))
		req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
		_, err := client.ExchangeDelegatedOperationAudience(context.Background(), req)
		return err
	}(), "the invoke binding beside it still exchanges")
	requireHeadlessOnlyRefusal(t, exchange("background", false), "background")
	requireHeadlessOnlyRefusal(t, exchange("background", true), "background")
}

func TestADelegationCannotRedeemAHeadlessOnlyBinding(t *testing.T) {
	installDelegationBindingService(t)
	registry, err := moduleregistrytest.Parse(strings.Replace(delegationBindingPrincipals,
		`"ingestservice":{"workload":{"service_account":"ingest","namespace":"acme-prod","container":"app"},"tenant":"`+delegationOrg+`","operation_audiences":{`,
		`"ingestservice":{"workload":{"service_account":"ingest","namespace":"acme-prod","container":"app"},"tenant":"`+delegationOrg+`","operation_audiences":{`+
			`"background":{"audience":"reports","headless_scopes":[{"resource_kind":"collections","actions":["write"]}]},`, 1))
	require.NoError(t, err)
	require.Contains(t, registry[business.ModulePrincipalID("ingestservice")].OperationAudiences, "background")
	service.SetModuleCapabilities(nil, nil, registry)

	t.Run("by reference", func(t *testing.T) {
		_, err := exchangeByDelegation(presentedAs(t, identityOf(t, "ingestservice")), "background")
		requireHeadlessOnlyRefusal(t, err, "background")
		_, err = exchangeByDelegation(presentedAs(t, identityOf(t, "ingestservice")), "ingest")
		require.NoError(t, err, "the delegation still reaches the invoke binding beside it")
	})

	t.Run("by a delegation-bearing parent", func(t *testing.T) {
		minted, err := mintSourceContext("docstore", moduleToken(t, "docstore"), business.SourceDelegationRef{DelegationID: delegationID})
		require.NoError(t, err)
		_, err = ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(presentedAs(t, identityOf(t, "ingestservice")),
			&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "background", ParentWorkContextToken: minted.GetToken()})
		requireHeadlessOnlyRefusal(t, err, "background")
	})
}
