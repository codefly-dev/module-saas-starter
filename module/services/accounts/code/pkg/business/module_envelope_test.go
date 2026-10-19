package business

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// The module envelope and the registry entry derived inside it, without a
// database: the administrator's grant intersected with what an applied module
// manifest asks for. The DSN-backed half — delivery to registry with no
// restart, narrowing, removal — is in pkg/infra/module_principal_registry_test.go.

const (
	testEnvelopeTenant   = "019f6bf7-5b4b-74e5-8c17-092259bb1661"
	testPresenceBinding  = "acme.test.reports"
	testModulePrefix     = "reports"
	testProducerAudience = "example-producer"
)

var testWorkload = ModuleWorkload{ServiceAccount: "reports", Namespace: "reports", Container: "app"}

// testEnvelopeDocument is a valid envelope: one principal's grant, wider than
// any one manifest asks for.
func testEnvelopeDocument() map[string]any {
	return map[string]any{
		"revision": float64(3),
		"principals": map[string]any{
			testModulePrefix: map[string]any{
				"grant": map[string]any{
					"tenant":              strings.ToUpper(testEnvelopeTenant),
					"queues":              []any{"reports.render", "reports.export"},
					"namespaces":          []any{"reports", "billing"},
					"external_namespaces": []any{"reports", "billing"},
					"resources":           []any{"reports", "ledgers"},
					"operation_audiences": map[string]any{
						"run": map[string]any{
							"audience": testProducerAudience,
							"invoke_scopes": []any{
								map[string]any{"resource_kind": "profiles", "actions": []any{"invoke", "read", "write"}, "resource_ids": []any{"standard"}},
							},
							"lookup_scopes":   []any{map[string]any{"resource_kind": "profiles", "actions": []any{"read"}, "resource_ids": []any{"standard"}}},
							"headless_scopes": []any{map[string]any{"resource_kind": "profiles", "actions": []any{"read"}, "resource_ids": []any{"standard"}}},
						},
						"unrequested": map[string]any{
							"audience":      "elsewhere",
							"invoke_scopes": []any{map[string]any{"resource_kind": "things", "actions": []any{"read"}}},
							"lookup_scopes": []any{map[string]any{"resource_kind": "things", "actions": []any{"read"}}},
						},
					},
				},
			},
		},
	}
}

func encodeEnvelope(t *testing.T, document map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(document)
	require.NoError(t, err)
	return raw
}

func parseTestEnvelope(t *testing.T) *ModuleEnvelope {
	t.Helper()
	envelope, err := ParseModuleEnvelope(encodeEnvelope(t, testEnvelopeDocument()))
	require.NoError(t, err)
	return envelope
}

// testManifest is a valid live module manifest over testPresenceBinding: one
// workload, and an authority request for the `run` binding with the given
// scopes per operation, a queue, a namespace and a resource kind of its own.
func testManifest(t *testing.T, operations map[string][]string) *solutionhost.ModuleManifest {
	t.Helper()
	manifest := &solutionhost.ModuleManifest{
		Schema: solutionhost.SchemaModuleManifestV1, Binding: testPresenceBinding, Alias: testModulePrefix,
		Kind: solutionhost.KindModule, Generation: 1,
		Host:      solutionhost.HostTarget{Coordinate: "acme-test", Domain: "acme-test"},
		Release:   solutionhost.Release{Publisher: "acme", Name: testModulePrefix, Version: "1.0.0"},
		Workloads: []solutionhost.Workload{{Service: "reports", Container: "app"}},
		Authority: &solutionhost.Authority{
			Principal:     testModulePrefix,
			Queues:        []string{"reports.render"},
			Namespaces:    []string{"reports"},
			ResourceKinds: []string{"reports"},
		},
	}
	if operations != nil {
		manifest.Authority.Bindings = []solutionhost.AuthorityBinding{{ID: "run", Audience: testProducerAudience, Operations: operations}}
	}
	require.NoError(t, manifest.Validate())
	return manifest
}

func TestModuleEnvelopeParsesTheGrantItsAdministratorWrote(t *testing.T) {
	envelope := parseTestEnvelope(t)
	require.Equal(t, uint64(3), envelope.Revision)
	entry, ok := envelope.Principals[testModulePrefix]
	require.True(t, ok)
	require.Equal(t, testModulePrefix, entry.Grant.Prefix)
	require.Equal(t, testEnvelopeTenant, entry.Grant.Tenant,
		"the tenant is canonicalized as the boot parser canonicalized it, so it matches its organization")
	require.Empty(t, entry.Grant.Workloads, "an envelope names no workload; the manifest does")
}

// TestModuleEnvelopeRefusesWhatItCannotMean: each case decodes and would read
// as a ceiling while bounding less than its author meant — or names what the
// module manifest now carries (workloads, builds, units), which an envelope
// never approves.
func TestModuleEnvelopeRefusesWhatItCannotMean(t *testing.T) {
	principal := func(document map[string]any) map[string]any {
		return document["principals"].(map[string]any)[testModulePrefix].(map[string]any)
	}
	grant := func(document map[string]any) map[string]any {
		return principal(document)["grant"].(map[string]any)
	}
	for name, mutate := range map[string]func(map[string]any){
		"a workload, which the manifest declares": func(d map[string]any) {
			grant(d)["workload"] = map[string]any{"service_account": "x", "namespace": "y", "container": "z"}
		},
		"approved builds, which leave the runtime": func(d map[string]any) { d["approved_builds"] = []any{} },
		"authority units, which the manifest asks for": func(d map[string]any) {
			principal(d)["bindings"] = []any{}
		},
		"workload identities": func(d map[string]any) { principal(d)["workload_identities"] = []any{} },
		"revision zero":       func(d map[string]any) { d["revision"] = float64(0) },
		"an unknown top key":  func(d map[string]any) { d["grants"] = []any{} },
		"a malformed tenant":  func(d map[string]any) { grant(d)["tenant"] = "acme" },
		"a host-sealed content": func(d map[string]any) {
			grant(d)["resources"] = []any{HostScopeRoles}
		},
		"an external namespace it does not bind": func(d map[string]any) {
			grant(d)["external_namespaces"] = []any{"elsewhere"}
		},
		"a principal keyed by principal id": func(d map[string]any) {
			principals := d["principals"].(map[string]any)
			principals[ModulePrincipalID(testModulePrefix)] = principals[testModulePrefix]
			delete(principals, testModulePrefix)
		},
	} {
		t.Run(name, func(t *testing.T) {
			document := testEnvelopeDocument()
			mutate(document)
			_, err := ParseModuleEnvelope(encodeEnvelope(t, document))
			require.ErrorIs(t, err, ErrModuleEnvelopeInvalid)
		})
	}
}

// The entry is the envelope's grant intersected with what the manifest asks
// for: every list keeps only what both name, and per operation only the scopes
// both name.
func TestTheDerivedEntryIsTheGrantBoundedByTheManifest(t *testing.T) {
	envelope := parseTestEnvelope(t)
	manifest := testManifest(t, map[string][]string{
		"invoke":   {"profiles:read", "profiles:delete"},
		"lookup":   {"profiles:read"},
		"headless": {"profiles:read"},
	})
	grant, omitted, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix], manifest, []ModuleWorkload{testWorkload})
	require.NoError(t, err)
	require.Empty(t, omitted)
	require.Equal(t, testPresenceBinding, grant.PresenceBinding)
	require.Equal(t, []string{"reports.render"}, grant.Queues)
	require.Equal(t, []string{"reports"}, grant.Namespaces)
	require.Equal(t, []string{"reports"}, grant.ExternalNamespaces)
	require.Equal(t, []string{"reports"}, grant.Resources)
	require.Equal(t, []ModuleWorkload{testWorkload}, grant.Workloads)
	require.Equal(t, map[string]ModuleOperationAudience{"run": {
		Audience:       testProducerAudience,
		InvokeScopes:   []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read"}, ResourceIDs: []string{"standard"}}},
		LookupScopes:   []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read"}, ResourceIDs: []string{"standard"}}},
		HeadlessScopes: []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read"}, ResourceIDs: []string{"standard"}}},
	}}, grant.OperationAudiences,
		"profiles:delete is asked for and not allowed; `unrequested` is allowed and not asked for — neither is granted")
}

func TestAnOperationBindingTheEnvelopeDoesNotAllowIsOmittedByName(t *testing.T) {
	envelope := parseTestEnvelope(t)
	for name, mutate := range map[string]func(*solutionhost.ModuleManifest){
		"an unknown binding": func(m *solutionhost.ModuleManifest) { m.Authority.Bindings[0].ID = "other" },
		"another audience":   func(m *solutionhost.ModuleManifest) { m.Authority.Bindings[0].Audience = "elsewhere" },
		"no invoke scope": func(m *solutionhost.ModuleManifest) {
			m.Authority.Bindings[0].Operations = map[string][]string{"lookup": {"profiles:read"}}
		},
		"nothing in common": func(m *solutionhost.ModuleManifest) {
			m.Authority.Bindings[0].Operations = map[string][]string{"invoke": {"profiles:delete"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			manifest := testManifest(t, map[string][]string{"invoke": {"profiles:read"}})
			mutate(manifest)
			grant, omitted, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix], manifest, []ModuleWorkload{testWorkload})
			require.NoError(t, err, "the principal stays; only the binding is omitted")
			require.Empty(t, grant.OperationAudiences)
			require.Len(t, omitted, 1)
			require.ErrorIs(t, omitted[0], ErrModulePrincipalBindingOmitted)
		})
	}
}

func TestAnEntryWithoutAWorkloadIsNeverInstalled(t *testing.T) {
	envelope := parseTestEnvelope(t)
	_, _, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix], testManifest(t, nil), nil)
	require.Error(t, err)
}

func TestTheDerivationOmitsEveryPrincipalSharingAWorkload(t *testing.T) {
	published := &publishedModuleRegistry{
		registry: ModulePrincipalRegistry{
			"a": {Prefix: "alpha", Workloads: []ModuleWorkload{testWorkload}},
			"b": {Prefix: "beta", Workloads: []ModuleWorkload{{Namespace: testWorkload.Namespace, ServiceAccount: testWorkload.ServiceAccount, Container: "other"}}},
			"c": {Prefix: "gamma", Workloads: []ModuleWorkload{{Namespace: "gamma", ServiceAccount: "gamma", Container: "app"}}},
		},
		sources: map[string]modulePrincipalSource{},
	}
	refusals := published.oneWorkloadOnePrincipal()
	require.Len(t, refusals, 1)
	require.ErrorIs(t, refusals[0], ErrModulePrincipalOmitted)
	require.Equal(t, []string{"c"}, keysOf(published.registry), "the container is not part of the key: a token names the account")
}

func keysOf(registry ModulePrincipalRegistry) []string {
	keys := make([]string, 0, len(registry))
	for key := range registry {
		keys = append(keys, key)
	}
	return keys
}

func TestSharedEnvelopeTenantIsNotAnOrganizationGrant(t *testing.T) {
	document := testEnvelopeDocument()
	document["principals"].(map[string]any)[testModulePrefix].(map[string]any)["grant"].(map[string]any)["tenant"] = ""
	envelope, err := ParseModuleEnvelope(encodeEnvelope(t, document))
	require.NoError(t, err)
	require.Empty(t, envelope.Principals[testModulePrefix].Grant.Tenant)
	tenant, err := canonicalModuleTenant("example", "")
	require.NoError(t, err)
	require.Empty(t, tenant)
	grant := ModulePrincipalGrant{Prefix: "example", Tenant: tenant}
	require.NoError(t, validateModulePrincipalCeiling("example", grant))
	require.True(t, moduleAvailableToOrganization(grant, testEnvelopeTenant))
	require.False(t, moduleAvailableToOrganization(grant, ""), "shared tenancy cannot authorize an unbound credential")
	_, err = canonicalModuleTenant("example", "not-an-organization")
	require.Error(t, err)
}

// The install-step queue is the host's own: its jobs are other solutions'
// install steps, and a module granted it could claim and complete one.
func TestTheInstallStepQueueIsGrantedToNoModule(t *testing.T) {
	document := testEnvelopeDocument()
	grant := document["principals"].(map[string]any)[testModulePrefix].(map[string]any)["grant"].(map[string]any)
	queues, _ := grant["queues"].([]any)
	grant["queues"] = append(queues, InstallationStepQueue)
	_, err := ParseModuleEnvelope(encodeEnvelope(t, document))
	require.ErrorIs(t, err, ErrModuleEnvelopeInvalid)
	require.ErrorContains(t, err, InstallationStepQueue)
}

// A manifest that asks for no operation still installs: the grant's lists are
// what both name, and none are.
func TestAManifestAskingForNoOperationGrantsNoConsumedOperations(t *testing.T) {
	wire := testEnvelopeDocument()
	wire["principals"].(map[string]any)[testModulePrefix] = map[string]any{"grant": map[string]any{"cross_tenant": true}}
	envelope, err := ParseModuleEnvelope(encodeEnvelope(t, wire))
	require.NoError(t, err)
	grant, omitted, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix], testManifest(t, nil), []ModuleWorkload{testWorkload})
	require.NoError(t, err)
	require.Empty(t, grant.OperationAudiences)
	require.Empty(t, grant.Queues)
	require.Empty(t, grant.Namespaces)
	require.Empty(t, grant.Resources)
	require.Empty(t, grant.ReadAudiences)
	require.True(t, grant.CrossTenant)
	// The ceiling is unchanged: this envelope allows none of what the manifest
	// declares, and the grant holds none of it.
	//
	// The removals are REPORTED, but to the operator's log rather than through
	// `omitted` — the pass's error is what a caller reads as "this derivation
	// did not complete", and a cell where several modules declare a content
	// kind the envelope never listed would otherwise fail every pass for ever
	// and freeze the registry at whatever it last derived. That is a measured
	// regression, not a hypothetical.
	require.Empty(t, omitted, "a dimension the envelope does not allow is logged, never a refusal of the pass")
}

// A manifest that asks for nothing on any axis published no contract — the
// shape of a module whose operations the composition prepares for it. For it
// the envelope, the owner's written consent, stands whole.
func TestAContractlessManifestHoldsTheEnvelopeWhole(t *testing.T) {
	envelope := parseTestEnvelope(t)
	manifest := testManifest(t, map[string][]string{"invoke": {"profiles:read"}})
	manifest.Authority = &solutionhost.Authority{Principal: testModulePrefix}
	grant, omitted, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix], manifest, []ModuleWorkload{testWorkload})
	require.NoError(t, err)
	require.Empty(t, omitted)
	ceiling := envelope.Principals[testModulePrefix].Grant
	require.Equal(t, ceiling.Queues, grant.Queues)
	require.Equal(t, ceiling.Namespaces, grant.Namespaces)
	require.Equal(t, ceiling.Resources, grant.Resources)
	require.Equal(t, ceiling.OperationAudiences, grant.OperationAudiences)
}
