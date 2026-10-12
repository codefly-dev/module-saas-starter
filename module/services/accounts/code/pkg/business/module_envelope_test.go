package business

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// The module envelope and the registry derived inside it, without a database:
// the projection core's ValidateAgainst is handed, and the intersection an
// entry is built from. The DSN-backed half — delivery to registry with no
// restart, narrowing, removal — is in pkg/infra/module_principal_registry_test.go.

const (
	testEnvelopeTenant   = "019f6bf7-5b4b-74e5-8c17-092259bb1661"
	testEnvelopeBuild    = solutionhost.ImageDigest("sha256:3880ab5504a3f436fead6e19fb23b641443747ab55faa3f63c7b7f91b610e28f")
	testPresenceBinding  = "acme.test.reports"
	testModulePrefix     = "reports"
	testProducerAudience = "example-producer"
)

func testUnitID(binding, operation string) string {
	return testPresenceBinding + ":" + binding + ":" + operation
}

const (
	testSPIFFEID  = "spiffe://acme.test/ns/acme-prod/sa/reports"
	testContainer = "app"
)

// testEnvelopeUnits are the whole units the test envelope admits: the run
// binding's invoke, lookup and headless operations, each narrower than the
// grant's audience allows.
func testEnvelopeUnits() []any {
	return []any{
		map[string]any{"id": testUnitID("run", "invoke"), "revision": float64(1), "scope": "profiles:read"},
		map[string]any{"id": testUnitID("run", "lookup"), "revision": float64(1), "scope": "profiles:read", "lookup_method": "directory"},
		map[string]any{"id": testUnitID("run", "headless"), "revision": float64(1), "scope": "profiles:read"},
	}
}

// testEnvelopeDocument is a valid envelope: the `run` binding admitted for
// invoke, lookup and headless, one allowed workload, scope ceilings on the
// module's own kind, and a grant wider than any one delivery asks for.
func testEnvelopeDocument() map[string]any {
	return map[string]any{
		"revision":        float64(3),
		"approved_builds": []any{string(testEnvelopeBuild)},
		"principals": map[string]any{
			testModulePrefix: map[string]any{
				"bindings":            testEnvelopeUnits(),
				"workload_identities": []any{map[string]any{"spiffe_id": testSPIFFEID, "container": testContainer}},
				"scope_ceilings":      []any{map[string]any{"resource_kind": "reports", "actions": []any{"read", "write"}}},
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

// testUnit is the unit delivery would write for one admitted operation —
// identical, field for field, to the one the envelope admits.
func testUnit(binding, operation string) solutionhost.AuthorityBinding {
	unit := solutionhost.AuthorityBinding{
		ID: testUnitID(binding, operation), Revision: 1, Audience: testProducerAudience, Scope: "profiles:read",
	}
	if operation == "lookup" {
		unit.LookupMethod = "directory"
	}
	return unit
}

// testAuthority is a valid authority document over testPresenceBinding granting
// the given units to the module principal, declaring queues, namespaces and a
// scope ceiling of its own.
func testAuthority(t *testing.T, units ...solutionhost.AuthorityBinding) *solutionhost.AuthorityDocument {
	t.Helper()
	document := &solutionhost.AuthorityDocument{
		Schema:           solutionhost.SchemaAuthorityV1,
		Authority:        testPresenceBinding + "-authority",
		Generation:       1,
		PresenceBinding:  testPresenceBinding,
		Host:             solutionhost.HostTarget{Coordinate: "acme-test", Component: "saas-host"},
		OwnershipDomain:  "acme-test",
		EnvelopeRevision: 3,
		ApprovedBuild:    testEnvelopeBuild,
		EffectiveFrom:    1,
		Queues:           []string{"reports.render"},
		Namespaces:       []string{"reports"},
		ScopeCeilings:    []solutionhost.ScopeCeiling{{ResourceKind: "reports", Actions: []string{"read", "write"}}},
		Principals:       []solutionhost.PrincipalAuthority{{Principal: testModulePrefix, Bindings: units}},
	}
	require.NoError(t, document.Validate())
	return document
}

var testWorkload = ModuleWorkload{ServiceAccount: "reports", Namespace: "acme-prod", Container: "app"}

func TestModuleEnvelopeParsesTheCeilingItsAdministratorWrote(t *testing.T) {
	envelope := parseTestEnvelope(t)
	require.Equal(t, uint64(3), envelope.Revision)
	require.Equal(t, []solutionhost.ImageDigest{testEnvelopeBuild}, envelope.ApprovedBuilds)
	entry, ok := envelope.Principals[testModulePrefix]
	require.True(t, ok)
	require.Equal(t, testModulePrefix, entry.Grant.Prefix)
	require.Equal(t, testEnvelopeTenant, entry.Grant.Tenant,
		"the tenant is canonicalized as the boot parser canonicalized it, so it matches its organization")
	require.Empty(t, entry.Grant.Workload, "an envelope names no workload; it is derived from presence")
}

// TestModuleEnvelopeRefusesWhatItCannotMean holds the envelope to the rules
// the boot-time registry parser applied, and to the ones a runtime ceiling adds.
// Each case is a document that decodes and would read as a ceiling while
// bounding less than its author meant.
func TestModuleEnvelopeRefusesWhatItCannotMean(t *testing.T) {
	principal := func(document map[string]any) map[string]any {
		return document["principals"].(map[string]any)[testModulePrefix].(map[string]any)
	}
	grant := func(document map[string]any) map[string]any {
		return principal(document)["grant"].(map[string]any)
	}
	for name, mutate := range map[string]func(map[string]any){
		"a workload, which is delivered and never typed": func(d map[string]any) {
			grant(d)["workload"] = map[string]any{"service_account": "x", "namespace": "y", "container": "z"}
		},
		"revision zero":         func(d map[string]any) { d["revision"] = float64(0) },
		"an unknown top key":    func(d map[string]any) { d["grants"] = []any{} },
		"a readable digest":     func(d map[string]any) { d["approved_builds"] = []any{"sha256:latest"} },
		"a malformed tenant":    func(d map[string]any) { grant(d)["tenant"] = "acme" },
		"a host-sealed content": func(d map[string]any) { grant(d)["resources"] = []any{HostScopeRoles} },
		"an external namespace it does not bind": func(d map[string]any) {
			grant(d)["external_namespaces"] = []any{"elsewhere"}
		},
		"a principal keyed by principal id": func(d map[string]any) {
			principals := d["principals"].(map[string]any)
			principals[ModulePrincipalID(testModulePrefix)] = principals[testModulePrefix]
			delete(principals, testModulePrefix)
		},
		"no admitted unit": func(d map[string]any) { principal(d)["bindings"] = []any{} },
		"a unit outside delivery's convention": func(d map[string]any) {
			principal(d)["bindings"] = []any{map[string]any{"id": testPresenceBinding + ":run:reconcile", "revision": float64(1), "scope": "profiles:read"}}
		},
		"a unit for an audience the grant does not declare": func(d map[string]any) {
			principal(d)["bindings"] = []any{map[string]any{"id": testUnitID("other", "invoke"), "revision": float64(1), "scope": "profiles:read"}}
		},
		"a headless unit with no headless scopes": func(d map[string]any) {
			audience := grant(d)["operation_audiences"].(map[string]any)["run"].(map[string]any)
			delete(audience, "headless_scopes")
		},
		"a unit scope wider than its operation allows": func(d map[string]any) {
			principal(d)["bindings"] = []any{map[string]any{"id": testUnitID("run", "lookup"), "revision": float64(1), "scope": "profiles:read,profiles:write"}}
		},
		"a unit scope that is not canonical": func(d map[string]any) {
			principal(d)["bindings"] = []any{map[string]any{"id": testUnitID("run", "invoke"), "revision": float64(1), "scope": "profiles:write,profiles:read"}}
		},
		"a unit with no revision": func(d map[string]any) {
			principal(d)["bindings"] = []any{map[string]any{"id": testUnitID("run", "invoke"), "scope": "profiles:read"}}
		},
		"a unit with a queue the grant does not allow": func(d map[string]any) {
			principal(d)["bindings"] = []any{map[string]any{"id": testUnitID("run", "invoke"), "revision": float64(1), "scope": "profiles:read", "queue": "payments.settle"}}
		},
		"no allowed workload": func(d map[string]any) { principal(d)["workload_identities"] = []any{} },
		"a workload identity that names no service account": func(d map[string]any) {
			principal(d)["workload_identities"] = []any{map[string]any{"spiffe_id": "spiffe://acme.test/workload/reports", "container": testContainer}}
		},
		"a scope ceiling with no action": func(d map[string]any) {
			principal(d)["scope_ceilings"] = []any{map[string]any{"resource_kind": "reports", "actions": []any{}}}
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

// TestTheProjectionAdmitsOnlyWhatTheEnvelopeListsForThisDocument is the one
// ceiling: core's containment rule, handed the projection of this record —
// built from the envelope alone.
func TestTheProjectionAdmitsOnlyWhatTheEnvelopeListsForThisDocument(t *testing.T) {
	envelope := parseTestEnvelope(t)

	inside := testAuthority(t, testUnit("run", "invoke"), testUnit("run", "lookup"))
	require.NoError(t, inside.ValidateAgainst(envelope.authorityEnvelopeFor(inside)))

	changed := func(mutate func(*solutionhost.AuthorityBinding)) *solutionhost.AuthorityDocument {
		unit := testUnit("run", "invoke")
		mutate(&unit)
		return testAuthority(t, unit)
	}
	for name, document := range map[string]*solutionhost.AuthorityDocument{
		// FINDING 7: every field of an admitted unit is the envelope's, so a
		// document that keeps the admitted id and changes anything else is
		// outside — it cannot supply its own ceiling.
		"a wider scope under an admitted id":         changed(func(u *solutionhost.AuthorityBinding) { u.Scope = "profiles:delete,profiles:read" }),
		"another revision under an admitted id":      changed(func(u *solutionhost.AuthorityBinding) { u.Revision = 2 }),
		"a binding key the envelope did not admit":   changed(func(u *solutionhost.AuthorityBinding) { u.BindingKey = "other-key" }),
		"a lookup method the envelope did not admit": changed(func(u *solutionhost.AuthorityBinding) { u.LookupMethod = "scan" }),
		"another audience":                           changed(func(u *solutionhost.AuthorityBinding) { u.Audience = "someone-else" }),
		"a queue the envelope did not admit":         changed(func(u *solutionhost.AuthorityBinding) { u.Queue = "reports.render" }),
		"a unit the envelope does not list":          testAuthority(t, testUnit("run", "invoke"), testUnit("audit", "invoke")),
		"a queue the grant does not allow": func() *solutionhost.AuthorityDocument {
			document := testAuthority(t, testUnit("run", "invoke"))
			document.Queues = []string{"payments.settle"}
			return document
		}(),
		"a wider scope ceiling": func() *solutionhost.AuthorityDocument {
			document := testAuthority(t, testUnit("run", "invoke"))
			document.ScopeCeilings = []solutionhost.ScopeCeiling{{ResourceKind: "reports", Actions: []string{"admin", "read", "write"}}}
			return document
		}(),
		"another principal claiming the module's unit": func() *solutionhost.AuthorityDocument {
			document := testAuthority(t, testUnit("run", "invoke"))
			document.Principals[0].Principal = "impostor"
			return document
		}(),
		"a build the envelope did not approve": func() *solutionhost.AuthorityDocument {
			document := testAuthority(t, testUnit("run", "invoke"))
			document.ApprovedBuild = solutionhost.ImageDigest("sha256:" + strings.Repeat("0", 64))
			return document
		}(),
		"another envelope revision": func() *solutionhost.AuthorityDocument {
			document := testAuthority(t, testUnit("run", "invoke"))
			document.EnvelopeRevision = 2
			return document
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, document.ValidateAgainst(envelope.authorityEnvelopeFor(document)),
				solutionhost.ErrOutsideEnvelope)
		})
	}

	t.Run("a unit filed under another presence binding", func(t *testing.T) {
		// Same unit id, delivered over a different presence binding: the unit
		// the envelope admitted names one instance of the module, never another.
		document := testAuthority(t, testUnit("run", "invoke"))
		document.PresenceBinding = "acme.test.reports-copy"
		require.ErrorIs(t, document.ValidateAgainst(envelope.authorityEnvelopeFor(document)),
			solutionhost.ErrOutsideEnvelope)
	})
}

// TestTheDerivedEntryIsTheEnvelopeBoundedByTheDocument is the intersection:
// never wider than the grant, never wider than the admitted unit.
func TestTheDerivedEntryIsTheEnvelopeBoundedByTheDocument(t *testing.T) {
	envelope := parseTestEnvelope(t)
	units := []solutionhost.AuthorityBinding{testUnit("run", "invoke"), testUnit("run", "lookup")}
	document := testAuthority(t, units...)
	require.NoError(t, document.ValidateAgainst(envelope.authorityEnvelopeFor(document)))

	grant, omitted, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix], document, units, testWorkload)
	require.NoError(t, err)
	require.Empty(t, omitted)

	require.Equal(t, testModulePrefix, grant.Prefix)
	require.Equal(t, testEnvelopeTenant, grant.Tenant)
	require.Equal(t, testWorkload, grant.Workload, "the workload is the one passed in, never read from the grant")
	require.Equal(t, []string{"reports.render"}, grant.Queues, "only queues the document declares too")
	require.Equal(t, []string{"reports"}, grant.Namespaces, "only namespaces the document declares too")
	require.Equal(t, []string{"reports"}, grant.ExternalNamespaces, "external delivery only within the namespaces kept")
	require.Equal(t, []string{"reports"}, grant.Resources, "only resources the document's own vocabulary names")

	require.Len(t, grant.OperationAudiences, 1, "an audience the document did not ask for is absent")
	run := grant.OperationAudiences["run"]
	require.Equal(t, testProducerAudience, run.Audience)
	require.Equal(t, []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read"}, ResourceIDs: []string{"standard"}}},
		run.InvokeScopes, "invoke is cut to the admitted unit's scope though the grant allows write, and resource ids stay the grant's")
	require.Equal(t, []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read"}, ResourceIDs: []string{"standard"}}}, run.LookupScopes)
	require.Nil(t, run.HeadlessScopes, "no headless unit was granted, so nothing may be minted headless")
	require.NoError(t, ValidateModulePrincipalGrant(testModulePrefix, grant))
}

// TestAnInvokeOnlyAudienceIsKept: a document granting the invoke unit and not
// the lookup one declares an audience that redeems no lookup. It is kept, with
// no lookup scopes — never filled in from the envelope, and never dropped as if
// the module had not declared it (the shape of a module that verifies, as the
// asking person, against a store it has nothing to look up in).
func TestAnInvokeOnlyAudienceIsKept(t *testing.T) {
	envelope := parseTestEnvelope(t)
	units := []solutionhost.AuthorityBinding{testUnit("run", "invoke")}
	document := testAuthority(t, units...)
	require.NoError(t, document.ValidateAgainst(envelope.authorityEnvelopeFor(document)))
	grant, omitted, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix], document, units, testWorkload)
	require.NoError(t, err)
	require.Empty(t, omitted)
	require.Contains(t, grant.OperationAudiences, "run")
	require.NotEmpty(t, grant.OperationAudiences["run"].InvokeScopes)
	require.Nil(t, grant.OperationAudiences["run"].LookupScopes, "no lookup unit, no lookup scopes")
}

// TestAHeadlessUnitGrantsOnlyWhatBothSidesAllow: headless is the module acting
// alone, so it exists only when the document grants the headless unit.
func TestAHeadlessUnitGrantsOnlyWhatBothSidesAllow(t *testing.T) {
	envelope := parseTestEnvelope(t)
	units := []solutionhost.AuthorityBinding{testUnit("run", "invoke"), testUnit("run", "lookup"), testUnit("run", "headless")}
	document := testAuthority(t, units...)
	grant, _, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix], document, units, testWorkload)
	require.NoError(t, err)
	require.Equal(t, []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read"}, ResourceIDs: []string{"standard"}}},
		grant.OperationAudiences["run"].HeadlessScopes)
}

// TestAnEntryWithoutAWorkloadIsNeverInstalled: the derived entry is validated
// whole, so a workload the presence could not resolve omits the principal.
func TestAnEntryWithoutAWorkloadIsNeverInstalled(t *testing.T) {
	envelope := parseTestEnvelope(t)
	units := []solutionhost.AuthorityBinding{testUnit("run", "invoke"), testUnit("run", "lookup")}
	document := testAuthority(t, units...)
	_, _, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix], document, units, ModuleWorkload{})
	require.ErrorContains(t, err, "must declare its workload identity")
}

// TestOnlyAnApprovedWorkloadBindsThePrincipal is finding 6: a presence running
// the approved build under a service account, namespace or container the
// envelope does not allow binds nothing.
func TestOnlyAnApprovedWorkloadBindsThePrincipal(t *testing.T) {
	envelope := parseTestEnvelope(t)
	entry := envelope.Principals[testModulePrefix]
	document := testAuthority(t, testUnit("run", "invoke"))

	workload, err := approvedWorkload(entry, []ModuleWorkloadIdentity{{SPIFFEID: testSPIFFEID, Container: testContainer}}, document)
	require.NoError(t, err)
	require.Equal(t, ModuleWorkload{ServiceAccount: "reports", Namespace: "acme-prod", Container: testContainer}, workload)

	for name, retargeted := range map[string]ModuleWorkloadIdentity{
		"another service account": {SPIFFEID: "spiffe://acme.test/ns/acme-prod/sa/intruder", Container: testContainer},
		"another namespace":       {SPIFFEID: "spiffe://acme.test/ns/elsewhere/sa/reports", Container: testContainer},
		"another container":       {SPIFFEID: testSPIFFEID, Container: "sidecar"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := approvedWorkload(entry, []ModuleWorkloadIdentity{retargeted}, document)
			require.ErrorIs(t, err, ErrModuleWorkloadNotApproved)
		})
	}
}

// TestSPIFFEIDsAreParsedStrictly is round 2's finding 5: the namespace and
// service account come from the exact /ns/<ns>/sa/<sa> path, never from a scan
// that can read the trust domain as the namespace, and a string that names no
// workload is refused — at envelope write time and wherever else it is read.
func TestSPIFFEIDsAreParsedStrictly(t *testing.T) {
	identity, err := ParseKubernetesSPIFFEID("spiffe://ns/ns/acme-prod/sa/reports")
	require.NoError(t, err)
	require.Equal(t, KubernetesSPIFFEID{TrustDomain: "ns", Namespace: "acme-prod", ServiceAccount: "reports"}, identity,
		"a trust domain named like a path segment is the trust domain, not the namespace")
	identity, err = ParseKubernetesSPIFFEID("spiffe://sa/ns/sa/sa/ns")
	require.NoError(t, err)
	require.Equal(t, "sa", identity.Namespace)
	require.Equal(t, "ns", identity.ServiceAccount)

	for _, malformed := range []string{
		"spiffe://acme.test:8443/ns/acme-prod/sa/reports",
		"spiffe:///ns/acme-prod/sa/reports",
		"spiffe://user@acme.test/ns/acme-prod/sa/reports",
		"spiffe://Acme.Test/ns/acme-prod/sa/reports",
		"spiffe://acme.test/sa/reports/ns/acme-prod",
		"spiffe://acme.test/ns/acme-prod/ns/other/sa/reports",
		"spiffe://acme.test/ns/acme-prod/sa/reports/extra",
		"spiffe://acme.test/ns/acme-prod/sa/",
		"spiffe://acme.test/ns/Acme/sa/reports",
		"spiffe://acme.test/ns/acme-prod/sa/reports?x=1",
		"spiffe://acme.test",
		"https://acme.test/ns/acme-prod/sa/reports",
		"acme-prod/reports",
	} {
		t.Run(malformed, func(t *testing.T) {
			_, err := ParseKubernetesSPIFFEID(malformed)
			require.ErrorIs(t, err, ErrSPIFFEIDMalformed)
			document := testEnvelopeDocument()
			document["principals"].(map[string]any)[testModulePrefix].(map[string]any)["workload_identities"] =
				[]any{map[string]any{"spiffe_id": malformed, "container": testContainer}}
			_, err = ParseModuleEnvelope(encodeEnvelope(t, document))
			require.ErrorIs(t, err, ErrModuleEnvelopeInvalid, "the envelope refuses it at write time")
		})
	}

	// The reviewer's case end to end: the approved string projects to the
	// namespace in its path.
	document := testEnvelopeDocument()
	document["principals"].(map[string]any)[testModulePrefix].(map[string]any)["workload_identities"] =
		[]any{map[string]any{"spiffe_id": "spiffe://ns/ns/acme-prod/sa/reports", "container": testContainer}}
	envelope, err := ParseModuleEnvelope(encodeEnvelope(t, document))
	require.NoError(t, err)
	require.Equal(t, ModuleWorkload{ServiceAccount: "reports", Namespace: "acme-prod", Container: testContainer},
		envelope.Principals[testModulePrefix].WorkloadIdentities[0].workload())
}

// TestOneWorkloadMapsToOnePrincipal: two principals allowing the same
// namespace and service account — whatever their containers — would let one
// module's pod token authorize the other's prefix, so the envelope is refused.
func TestOneWorkloadMapsToOnePrincipal(t *testing.T) {
	document := testEnvelopeDocument()
	principals := document["principals"].(map[string]any)
	other := map[string]any{}
	for key, value := range principals[testModulePrefix].(map[string]any) {
		other[key] = value
	}
	other["workload_identities"] = []any{map[string]any{"spiffe_id": testSPIFFEID, "container": "another-container"}}
	other["bindings"] = []any{map[string]any{"id": "acme.test.other:run:invoke", "revision": float64(1), "scope": "profiles:read"}}
	principals["other"] = other
	_, err := ParseModuleEnvelope(encodeEnvelope(t, document))
	require.ErrorIs(t, err, ErrModuleEnvelopeInvalid)
	require.ErrorContains(t, err, "one workload identity maps to exactly one principal")

	other["workload_identities"] = []any{map[string]any{"spiffe_id": "spiffe://acme.test/ns/acme-prod/sa/other", "container": testContainer}}
	_, err = ParseModuleEnvelope(encodeEnvelope(t, document))
	require.NoError(t, err, "a different service account is a different workload")
}

// TestTheDerivationOmitsEveryPrincipalSharingAWorkload: the same rule at
// derivation — whatever reached the registry, no workload maps to two
// principals.
func TestTheDerivationOmitsEveryPrincipalSharingAWorkload(t *testing.T) {
	shared := ModuleWorkload{ServiceAccount: "reports", Namespace: "acme-prod", Container: "app"}
	published := &publishedModuleRegistry{
		registry: ModulePrincipalRegistry{
			"a": {Prefix: "first", Workload: shared},
			"b": {Prefix: "second", Workload: ModuleWorkload{ServiceAccount: "reports", Namespace: "acme-prod", Container: "other"}},
			"c": {Prefix: "third", Workload: ModuleWorkload{ServiceAccount: "third", Namespace: "acme-prod", Container: "app"}},
		},
		sources: map[string]modulePrincipalSource{},
	}
	refusals := published.oneWorkloadOnePrincipal()
	require.Len(t, refusals, 1)
	require.ErrorIs(t, refusals[0], ErrModulePrincipalOmitted)
	require.Equal(t, []string{"c"}, slices.Collect(maps.Keys(published.registry)))
}

func TestSharedEnvelopeTenantIsNotAnOrganizationGrant(t *testing.T) {
	document := testEnvelopeDocument()
	document["principals"].(map[string]any)[testModulePrefix].(map[string]any)["grant"].(map[string]any)["tenant"] = ""
	raw, err := json.Marshal(document)
	require.NoError(t, err)
	envelope, err := ParseModuleEnvelope(raw)
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
