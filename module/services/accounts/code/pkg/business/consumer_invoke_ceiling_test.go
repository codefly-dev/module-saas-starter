package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConsumerCeilingAdmitsOnlyInsideOneDeclaredBinding(t *testing.T) {
	bindings := map[string]ModuleOperationAudience{
		"documents-read": {Audience: "documents", InvokeScopes: []ModuleOperationScope{
			{ResourceKind: "evidence", Actions: []string{"read"}},
		}},
		"documents-notes": {Audience: "documents", InvokeScopes: []ModuleOperationScope{
			{ResourceKind: "notes", Actions: []string{"write"}},
		}},
		"rows": {Audience: "rows", InvokeScopes: []ModuleOperationScope{
			{ResourceKind: "evidence", Actions: []string{"read"}, ResourceIDs: []string{"a", "b"}},
		}},
		// A binding the installation scope emptied is no declaration at all.
		"emptied": {Audience: "billing"},
	}
	for name, probe := range map[string]struct {
		audience         string
		scopes           []ModuleOperationScope
		declared, inside bool
	}{
		"inside one binding": {"documents", []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}, true, true},
		"inside the other":   {"documents", []ModuleOperationScope{{ResourceKind: "notes", Actions: []string{"write"}}}, true, true},
		// Each half is declared, by a different binding. Their union is not a
		// declaration anyone made.
		"split across two bindings": {"documents", []ModuleOperationScope{
			{ResourceKind: "evidence", Actions: []string{"read"}},
			{ResourceKind: "notes", Actions: []string{"write"}},
		}, true, false},
		"an action not declared":   {"documents", []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"write"}}}, true, false},
		"named resources, inside":  {"rows", []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}, ResourceIDs: []string{"a"}}}, true, true},
		"named resources, outside": {"rows", []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}, ResourceIDs: []string{"c"}}}, true, false},
		// "every evidence row" is not inside "rows a and b".
		"a whole kind under named resources": {"rows", []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}, true, false},
		"an audience nothing declares":       {"example", []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}, false, false},
		"a binding with no invoke scopes":    {"billing", nil, false, false},
		"no audience":                        {"", nil, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			declared, inside := ConsumerCeilingAdmits(bindings, probe.audience, probe.scopes)
			require.Equal(t, probe.declared, declared, "declared")
			require.Equal(t, probe.inside, inside, "inside")
		})
	}
}

// consumerStateStore reads one installation's delivered principal.
type consumerStateStore struct {
	Store
	state *InstallationAuthorityState
}

func (s consumerStateStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s consumerStateStore) InstallationAuthorityState(context.Context, string, string) (*InstallationAuthorityState, error) {
	return s.state, nil
}

func (consumerStateStore) NarrowInstallationScope(context.Context, string, string, uint64, InstallationScope, ModulePrincipalRegistry, uint64) error {
	return nil
}

// liveUnder reports the consumer's principal as installed under one id.
type liveUnder struct {
	service      *Service
	installation string
}

func (l liveUnder) LiveModuleAuthority(_ context.Context, principal, _, _ string) (*LiveModuleAuthority, error) {
	grant := l.service.ModulePrincipals()[principal]
	return &LiveModuleAuthority{InstallationID: l.installation, RegistryBindingID: grant.PresenceBinding, Scope: FullInstallationScope(grant)}, nil
}

func (liveUnder) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

// The ceiling is read under the installation the perimeter NAMED. The same
// principal live under another installation is consent nobody gave this
// credential, and it is refused rather than read.
func TestConsumerInvokeCeilingIsReadUnderTheNamedInstallation(t *testing.T) {
	const org = "11111111-1111-4111-8111-111111111111"
	principal := ModulePrincipalID("example-consumer")
	declared := map[string]ModuleOperationAudience{"documents": {Audience: "documents", InvokeScopes: []ModuleOperationScope{
		{ResourceKind: "evidence", Actions: []string{"read"}},
	}}}
	for name, probe := range map[string]struct {
		live string
		want codes.Code
	}{
		"the named installation": {"installation-named", codes.OK},
		"another installation":   {"installation-other", codes.Unauthenticated},
	} {
		t.Run(name, func(t *testing.T) {
			svc, err := NewService(consumerStateStore{state: &InstallationAuthorityState{PrincipalID: principal, BindingID: "binding", Scope: InstallationScope{Capabilities: true}}})
			require.NoError(t, err)
			svc.SetModulePrincipals(ModulePrincipalRegistry{principal: {Prefix: "example-consumer", PresenceBinding: "binding", OperationAudiences: declared}})
			svc.SetModuleAuthorityReads(liveUnder{service: svc, installation: probe.live}, nil)

			ceiling, err := svc.ConsumerInvokeCeiling(context.Background(), org, "installation-named")

			require.Equal(t, probe.want, status.Code(err), "err=%v", err)
			if probe.want == codes.OK {
				require.Len(t, ceiling.Consented, 1)
				require.Equal(t, declared["documents"].Audience, ceiling.Consented["documents"].Audience)
				require.Equal(t, declared["documents"].InvokeScopes, ceiling.Consented["documents"].InvokeScopes)
			} else {
				require.Nil(t, ceiling)
			}
		})
	}
}

// derivedCeiling is a consumer's operation audiences DERIVED the way production
// derives them — envelope ∩ the applied manifest's request — not hand-written.
func derivedCeiling(t *testing.T, operations map[string][]string) map[string]ModuleOperationAudience {
	t.Helper()
	envelope := parseTestEnvelope(t)
	grant, omitted, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix],
		testManifest(t, operations), []ModuleWorkload{testWorkload})
	require.NoError(t, err)
	require.Empty(t, omitted)
	return grant.OperationAudiences
}

// What the consumer declares is bounded by the envelope AND by what its
// document asked for: the envelope allows profiles:{invoke,read,write}, the
// delivered unit asks profiles:read, and an audience the envelope lists that the
// document never asked for is not declared.
func TestTheDeclaredCeilingIsBoundedByTheEnvelopeAndTheDocument(t *testing.T) {
	ceiling := derivedCeiling(t, map[string][]string{"invoke": {"profiles:read"}, "lookup": {"profiles:read"}})
	read := []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read"}, ResourceIDs: []string{"standard"}}}

	declared, inside := ConsumerCeilingAdmits(ceiling, testProducerAudience, read)
	require.True(t, declared)
	require.True(t, inside, "the declared, envelope-allowed read is admitted")

	_, inside = ConsumerCeilingAdmits(ceiling, testProducerAudience,
		[]ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"write"}, ResourceIDs: []string{"standard"}}})
	require.False(t, inside, "write: the envelope allows it, the document did not ask")

	_, inside = ConsumerCeilingAdmits(ceiling, testProducerAudience,
		[]ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read"}}})
	require.False(t, inside, "the grant is bound to resource ids, so an unbounded request is outside")

	declared, _ = ConsumerCeilingAdmits(ceiling, "elsewhere", []ModuleOperationScope{{ResourceKind: "things", Actions: []string{"read"}}})
	require.False(t, declared, "the envelope lists `elsewhere`; the consumer never asked for it")
}

// An INVOKE-ONLY declaration — the shape of a binding that verifies as the
// asking person and has nothing to look up — reaches the ceiling. It used to be
// dropped by the reconciler and then refused as undeclared.
func TestAnInvokeOnlyDeclarationReachesTheCeiling(t *testing.T) {
	ceiling := derivedCeiling(t, map[string][]string{"invoke": {"profiles:read"}})

	declared, inside := ConsumerCeilingAdmits(ceiling, testProducerAudience,
		[]ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read"}, ResourceIDs: []string{"standard"}}})

	require.True(t, declared, "the consumer's delivered authority declares invoke at this audience")
	require.True(t, inside)
}

// No consumer can declare itself, so a ceiling read from the declaration never
// admits a capability addressed to the consumer; Admit says so by name.
func TestAConsumerCannotDeclareItself(t *testing.T) {
	err := validateOperationAudiences(testModulePrefix, map[string]ModuleOperationAudience{"self": {
		Audience:     testModulePrefix,
		InvokeScopes: []ModuleOperationScope{{ResourceKind: "reports", Actions: []string{"read"}}},
	}})
	require.Error(t, err)
}

// A SELF-ADMITTING binding — headless work the consumer admits under its own
// prefix — is a valid declaration and carries invoke scopes, and it still never
// admits a confined mint addressed to the consumer: the own-audience refusal
// comes before any binding is read.
func TestASelfAdmittingBindingNeverAdmitsAConfinedMint(t *testing.T) {
	evidence := []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}
	self := map[string]ModuleOperationAudience{"triggers": {
		Audience: "example-consumer", InvokeScopes: evidence, HeadlessScopes: evidence,
	}}
	require.NoError(t, validateOperationAudiences("example-consumer", self), "the declaration itself is valid")
	ceiling := ConsumerCeiling{Declared: self, Consented: self, Own: []string{"example-consumer"}}

	require.Equal(t, ConsumerCeilingOwnAudience, ceiling.Admit("example-consumer", evidence))
}

func TestConsumerCeilingVerdicts(t *testing.T) {
	evidence := []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}
	declared := map[string]ModuleOperationAudience{
		"documents": {Audience: "documents", InvokeScopes: evidence},
		"rows":      {Audience: "rows", InvokeScopes: evidence},
	}
	ceiling := ConsumerCeiling{
		Declared:  declared,
		Consented: map[string]ModuleOperationAudience{"documents": declared["documents"]},
		Own:       []string{"example-consumer", SolutionAudience("binding")},
	}
	for name, probe := range map[string]struct {
		audience string
		scopes   []ModuleOperationScope
		want     ConsumerCeilingVerdict
	}{
		"declared and consented":           {"documents", evidence, ConsumerCeilingAdmitted},
		"declared after install":           {"rows", evidence, ConsumerCeilingNotConsented},
		"declared, scopes outside":         {"documents", []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"write"}}}, ConsumerCeilingOutsideDeclared},
		"never declared":                   {"example", evidence, ConsumerCeilingUndeclared},
		"the consumer's module audience":   {"example-consumer", evidence, ConsumerCeilingOwnAudience},
		"the consumer's solution audience": {SolutionAudience("binding"), evidence, ConsumerCeilingOwnAudience},
		"no audience":                      {"", evidence, ConsumerCeilingUndeclared},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, probe.want, ceiling.Admit(probe.audience, probe.scopes))
		})
	}
}

// The re-check after signing decides again and compares the consumer's
// declaration and this installation's consent, not the registry's identity: a
// republish that changed nothing passes; a withdrawn binding, a moved presence
// binding or a principal that left the registry refuses.
func TestConsumerCeilingStillCurrent(t *testing.T) {
	const org = "11111111-1111-4111-8111-111111111111"
	principal := ModulePrincipalID("example-consumer")
	declared := map[string]ModuleOperationAudience{"documents": {Audience: "documents", InvokeScopes: []ModuleOperationScope{
		{ResourceKind: "evidence", Actions: []string{"read"}},
	}}}
	svc, err := NewService(consumerStateStore{state: &InstallationAuthorityState{PrincipalID: principal, BindingID: "binding", Scope: InstallationScope{Capabilities: true}}})
	require.NoError(t, err)
	svc.SetModuleAuthorityReads(liveUnder{service: svc, installation: "installation-named"}, nil)
	svc.SetModulePrincipals(ModulePrincipalRegistry{principal: {Prefix: "example-consumer", PresenceBinding: "binding", OperationAudiences: declared}})
	decided, err := svc.ConsumerInvokeCeiling(context.Background(), org, "installation-named")
	require.NoError(t, err)

	svc.SetModulePrincipals(ModulePrincipalRegistry{principal: {Prefix: "example-consumer", PresenceBinding: "binding", OperationAudiences: declared}})
	require.NoError(t, svc.ConsumerCeilingStillCurrent(context.Background(), decided), "republished, unchanged")

	svc.SetModulePrincipals(ModulePrincipalRegistry{principal: {Prefix: "example-consumer", PresenceBinding: "binding"}})
	require.Equal(t, codes.Aborted, status.Code(svc.ConsumerCeilingStillCurrent(context.Background(), decided)), "binding withdrawn")

	svc.SetModulePrincipals(ModulePrincipalRegistry{principal: {Prefix: "example-consumer", PresenceBinding: "binding-2", OperationAudiences: declared}})
	require.Equal(t, codes.Aborted, status.Code(svc.ConsumerCeilingStillCurrent(context.Background(), decided)), "presence binding moved")

	svc.SetModulePrincipals(ModulePrincipalRegistry{})
	require.Equal(t, codes.Aborted, status.Code(svc.ConsumerCeilingStillCurrent(context.Background(), decided)), "principal gone")
}
