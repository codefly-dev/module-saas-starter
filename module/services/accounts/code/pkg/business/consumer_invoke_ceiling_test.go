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
			svc, err := NewService(consumerStateStore{state: &InstallationAuthorityState{PrincipalID: principal, BindingID: "binding"}})
			require.NoError(t, err)
			svc.SetModulePrincipals(ModulePrincipalRegistry{principal: {Prefix: "example-consumer", PresenceBinding: "binding", OperationAudiences: declared}})
			svc.SetModuleAuthorityReads(liveUnder{service: svc, installation: probe.live}, nil)

			ceiling, err := svc.ConsumerInvokeCeiling(context.Background(), org, "installation-named")

			require.Equal(t, probe.want, status.Code(err), "err=%v", err)
			if probe.want == codes.OK {
				require.Len(t, ceiling, 1)
				require.Equal(t, declared["documents"].Audience, ceiling["documents"].Audience)
				require.Equal(t, declared["documents"].InvokeScopes, ceiling["documents"].InvokeScopes)
			} else {
				require.Nil(t, ceiling)
			}
		})
	}
}
