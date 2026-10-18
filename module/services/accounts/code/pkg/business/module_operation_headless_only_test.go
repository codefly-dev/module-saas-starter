package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A HEADLESS-ONLY binding (#1156): background work no person asks for —
// recording evidence, exporting spans, embedding — declares headless scopes and
// no invoke. It is granted, minted headless, and never redeemed on a person's
// behalf.

var headlessEvidence = []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"commit", "record"}}}

func TestAHeadlessOnlyBindingIsAWholeDeclarationAndCarriesNothingAPersonRedeems(t *testing.T) {
	invoke := []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"commit", "read", "record"}}}
	lookup := []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}

	require.NoError(t, validateOperationAudiences("example", map[string]ModuleOperationAudience{
		"evidence": {Audience: "tracing", HeadlessScopes: headlessEvidence},
	}))
	require.NoError(t, validateOperationAudiences("example", map[string]ModuleOperationAudience{
		"self": {Audience: "example", HeadlessScopes: headlessEvidence},
	}), "headless work is self-admitting, so it may name its own module")
	require.True(t, ModuleOperationAudience{Audience: "tracing", HeadlessScopes: headlessEvidence}.headlessOnly())

	for name, binding := range map[string]ModuleOperationAudience{
		"neither invoke nor headless":        {Audience: "tracing"},
		"empty invoke beside headless":       {Audience: "tracing", InvokeScopes: []ModuleOperationScope{}, HeadlessScopes: headlessEvidence},
		"empty headless":                     {Audience: "tracing", HeadlessScopes: []ModuleOperationScope{}},
		"non-canonical headless":             {Audience: "tracing", HeadlessScopes: []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"record", "commit"}}}},
		"lookup without invoke":              {Audience: "tracing", LookupScopes: lookup, HeadlessScopes: headlessEvidence},
		"source delegation without invoke":   {Audience: "tracing", HeadlessScopes: headlessEvidence, SourceDelegationScopes: headlessEvidence},
		"self audience without headless use": {Audience: "example", InvokeScopes: invoke},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateOperationAudiences("example", map[string]ModuleOperationAudience{"evidence": binding}))
		})
	}

	// Beside invoke, the rules are unchanged: headless stays within invoke.
	require.Error(t, validateOperationAudiences("example", map[string]ModuleOperationAudience{"evidence": {
		Audience: "tracing", InvokeScopes: lookup, HeadlessScopes: headlessEvidence,
	}}))
	require.False(t, ModuleOperationAudience{Audience: "tracing", InvokeScopes: invoke, HeadlessScopes: headlessEvidence}.headlessOnly())
}

// The envelope ceiling accepts an audience that allows headless use only, and
// refuses one that pairs it with lookup or source delegation.
func TestTheEnvelopeCeilingAcceptsAHeadlessOnlyAudience(t *testing.T) {
	document := func(audience map[string]any) []byte {
		wire := testEnvelopeDocument()
		grant := wire["principals"].(map[string]any)[testModulePrefix].(map[string]any)["grant"].(map[string]any)
		grant["operation_audiences"] = map[string]any{"run": audience}
		return encodeEnvelope(t, wire)
	}
	headless := []any{map[string]any{"resource_kind": "profiles", "actions": []any{"read"}, "resource_ids": []any{"standard"}}}

	envelope, err := ParseModuleEnvelope(document(map[string]any{"audience": testProducerAudience, "headless_scopes": headless}))
	require.NoError(t, err)
	allowed := envelope.Principals[testModulePrefix].Grant.OperationAudiences["run"]
	require.Nil(t, allowed.InvokeScopes)
	require.True(t, allowed.headlessOnly())

	_, err = ParseModuleEnvelope(document(map[string]any{"audience": testProducerAudience, "headless_scopes": headless, "lookup_scopes": headless}))
	require.ErrorIs(t, err, ErrModuleEnvelopeInvalid)
	_, err = ParseModuleEnvelope(document(map[string]any{"audience": testProducerAudience, "invoke_scopes": []any{}, "headless_scopes": headless}))
	require.ErrorIs(t, err, ErrModuleEnvelopeInvalid, "an empty invoke list is a misconfiguration, not a headless-only audience")
}

// A manifest that asks only headless operations is granted its headless scopes
// within the envelope, and nothing else on that binding.
func TestAManifestAskingOnlyHeadlessIsGrantedAHeadlessOnlyBinding(t *testing.T) {
	envelope := parseTestEnvelope(t)
	standardRead := []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read"}, ResourceIDs: []string{"standard"}}}
	for name, operations := range map[string]map[string][]string{
		"headless alone":                     {"headless": {"profiles:read"}},
		"headless with lookup":               {"headless": {"profiles:read"}, "lookup": {"profiles:read"}},
		"invoke the envelope does not allow": {"headless": {"profiles:read"}, "invoke": {"profiles:delete"}},
	} {
		t.Run(name, func(t *testing.T) {
			grant, omitted, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix],
				testManifest(t, operations), []ModuleWorkload{testWorkload})
			require.NoError(t, err)
			require.Empty(t, omitted)
			require.Equal(t, map[string]ModuleOperationAudience{"run": {
				Audience: testProducerAudience, HeadlessScopes: standardRead,
			}}, grant.OperationAudiences, "no invoke, so no lookup and no source delegation either")
		})
	}

	// Headless beyond the envelope leaves nothing to grant, and the reason is
	// recorded rather than the binding silently dropped.
	grant, omitted, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix],
		testManifest(t, map[string][]string{"headless": {"profiles:write"}}), []ModuleWorkload{testWorkload})
	require.NoError(t, err)
	require.Empty(t, grant.OperationAudiences)
	require.Len(t, omitted, 1)
	require.ErrorIs(t, omitted[0], ErrModulePrincipalBindingOmitted)

	// A headless-only envelope audience bounds an invoke-asking manifest to
	// headless use: invoke and lookup are never granted past the ceiling.
	wire := testEnvelopeDocument()
	ceiling := wire["principals"].(map[string]any)[testModulePrefix].(map[string]any)["grant"].(map[string]any)
	ceiling["operation_audiences"] = map[string]any{"run": map[string]any{
		"audience":        testProducerAudience,
		"headless_scopes": []any{map[string]any{"resource_kind": "profiles", "actions": []any{"read"}, "resource_ids": []any{"standard"}}},
	}}
	headlessCeiling, err := ParseModuleEnvelope(encodeEnvelope(t, wire))
	require.NoError(t, err)
	grant, omitted, err = deriveModulePrincipalGrant(testModulePrefix, headlessCeiling.Principals[testModulePrefix],
		testManifest(t, map[string][]string{"invoke": {"profiles:read"}, "lookup": {"profiles:read"}, "headless": {"profiles:read"}}),
		[]ModuleWorkload{testWorkload})
	require.NoError(t, err)
	require.Empty(t, omitted)
	require.Equal(t, map[string]ModuleOperationAudience{"run": {Audience: testProducerAudience, HeadlessScopes: standardRead}}, grant.OperationAudiences)

	// A headless-only binding declares nothing a confined (person's) mint can use.
	declared, inside := ConsumerCeilingAdmits(grant.OperationAudiences, testProducerAudience, standardRead)
	require.False(t, declared)
	require.False(t, inside)
}

// Consent keeps a headless-only binding on its headless scopes, and never turns
// an invoke binding whose invoke it withholds into a headless-only one.
func TestInstallationConsentKeepsAHeadlessOnlyBindingHeadlessOnly(t *testing.T) {
	invoke := []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"commit", "read", "record"}}}
	grant := ModulePrincipalGrant{Prefix: "example", OperationAudiences: map[string]ModuleOperationAudience{
		"evidence": {Audience: "tracing", HeadlessScopes: headlessEvidence},
		"both":     {Audience: "tracing", InvokeScopes: invoke, HeadlessScopes: headlessEvidence},
	}}

	full := intersectInstallationScope(grant, FullInstallationScope(grant))
	require.Equal(t, grant.OperationAudiences, full.OperationAudiences)

	consent := FullInstallationScope(grant)
	both := consent.OperationAudiences["both"]
	both.InvokeScopes = nil
	consent.OperationAudiences["both"] = both
	narrowed := intersectInstallationScope(grant, consent)
	require.Equal(t, map[string]ModuleOperationAudience{"evidence": {Audience: "tracing", HeadlessScopes: headlessEvidence}}, narrowed.OperationAudiences)

	withdrawn := FullInstallationScope(grant)
	evidence := withdrawn.OperationAudiences["evidence"]
	evidence.HeadlessScopes = nil
	withdrawn.OperationAudiences["evidence"] = evidence
	require.NotContains(t, intersectInstallationScope(grant, withdrawn).OperationAudiences, "evidence")
}

// THE SECURITY PROPERTY: the binding is minted headless and is refused, by
// name, on every person-delegated path.
func TestAHeadlessOnlyBindingMintsHeadlessAndIsNeverRedeemedForAPerson(t *testing.T) {
	const tenant = "019f6bf7-5b4b-74e5-8c17-092259bb1661"
	registry, err := parseRegistryFixture(`{"example":{"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},"tenant":"` + tenant + `","operation_audiences":{` +
		`"evidence":{"audience":"tracing","headless_scopes":[{"resource_kind":"evidence","actions":["commit","record"]}]}}}}`)
	require.NoError(t, err)
	s := &Service{}
	withCurrentAuthority(s)
	s.SetModulePrincipals(registry)
	token := bindTestModule(s, "example")
	ctx := context.Background()
	id := ModulePrincipalID("example")

	authority, err := s.ModuleAuthorizeOperationContext(ctx, "example", token, "evidence")
	require.NoError(t, err)
	require.Equal(t, "tracing", authority.Audience)
	require.Equal(t, headlessEvidence, authority.Scopes)
	handled, err := s.CheckModuleOperationContextRevision(ctx, tenant, id, authority.Revision,
		[]ModuleOperationRevisionSubject{{PrincipalID: id, Scopes: headlessEvidence}})
	require.True(t, handled)
	require.NoError(t, err, "a consumer confirms the headless context against the binding")

	caller := ModuleCaller{PrincipalID: id, BoundOrg: tenant}
	_, err = s.ModuleOperationAudience(ctx, caller, tenant, "example", "evidence")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.ErrorContains(t, err, `"evidence" is headless-only`)

	delegation := &SourceDelegation{ID: "d-1", OrgID: tenant, ModulePrefix: "other", BindingID: "sync"}
	_, err = s.ModuleOperationAudienceForDelegation(ctx, caller, delegation, "example", "evidence")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.ErrorContains(t, err, `"evidence" is headless-only`)
}
