package business

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// A module that installs prepared bindings asks for one operation binding per
// endpoint operation it calls, so its binding count grows with every module a
// solution composes. These tests hold the host to the bound that is real — the
// installation scope a grant must fit in — and to naming its refusal where an
// operator reads delivery.

// derivedBindingsManifest is testManifest asking for count derived bindings,
// each spelled as the composition spells a prepared key, addressed to one of
// four producer modules, and asking for kinds resource kinds with invoke and a
// read-only lookup on each: the shape a tool-calling module's request takes.
func derivedBindingsManifest(t *testing.T, count, kinds int) *solutionhost.ModuleManifest {
	t.Helper()
	manifest := testManifest(t, nil)
	modules := []string{"documents", "records", "compute", "annotations"}
	for i := range count {
		module := modules[i%len(modules)]
		operations := map[string][]string{}
		for k := range kinds {
			kind := fmt.Sprintf("%s.kind%02d", module, k)
			operations["invoke"] = append(operations["invoke"], kind+":read", kind+":write")
			operations["lookup"] = append(operations["lookup"], kind+":read")
		}
		manifest.Authority.Bindings = append(manifest.Authority.Bindings, solutionhost.AuthorityBinding{
			ID:         fmt.Sprintf("%s__%s_CONNECT_OPERATION_%04d", strings.ToUpper(module), strings.ToUpper(module), i),
			Audience:   module,
			Operations: operations,
		})
	}
	require.NoError(t, manifest.Validate())
	return manifest
}

// The bound a grant is held to is the one the installations contract puts on
// the scope document a person writes, on install and on narrowing alike.
func TestTheInstallableGrantBoundIsTheInstallationScopeContract(t *testing.T) {
	require.Equal(t, 1<<20, installationScopeMaxBytes)
	require.Equal(t, installationScopeMaxBytes, contractStringMaxBytes(&gen.NarrowInstallationRequest{}, "scope"),
		"narrowing writes the same scope document install does, so it carries the same bound")
}

// More operation bindings than the count of 32 this host once refused past —
// with and without an administrative envelope — reach the registry whole.
func TestAModuleWithManyDerivedBindingsIsDelivered(t *testing.T) {
	const count = 96
	asking := derivedBindingsManifest(t, count, 2)

	t.Run("no envelope: the manifest is the ceiling", func(t *testing.T) {
		host := deliveredHost(t, noEnvelopeWritten{}, ceilingReleaseKey, asking)
		require.NoError(t, host.modulePrincipals.RunOnce(context.Background()))
		grant := host.service.ModulePrincipals()[ModulePrincipalID(testModulePrefix)]
		require.Len(t, grant.OperationAudiences, count)
		require.Empty(t, host.modulePrincipals.WithheldAuthorityReasons())
	})

	t.Run("an envelope listing every binding in module_actor mode", func(t *testing.T) {
		// The entry shape the composition writes for each prepared binding a
		// module installs: audience, kinds and actions, module_actor.
		audiences := map[string]any{}
		for _, binding := range asking.Authority.Bindings {
			ceiling := manifestCeiling(&solutionhost.Authority{Bindings: []solutionhost.AuthorityBinding{binding}}).Grant.OperationAudiences[binding.ID]
			audiences[binding.ID] = map[string]any{
				"delegation_mode": DelegationModeModuleActor, "audience": ceiling.Audience,
				"invoke_scopes": ceiling.InvokeScopes, "lookup_scopes": ceiling.LookupScopes,
			}
		}
		raw, err := json.Marshal(map[string]any{"revision": 1, "principals": map[string]any{testModulePrefix: map[string]any{"grant": map[string]any{
			"queues": asking.Authority.Queues, "namespaces": asking.Authority.Namespaces, "resources": asking.Authority.ResourceKinds,
			"operation_audiences": audiences,
		}}}})
		require.NoError(t, err)
		envelope, err := ParseModuleEnvelope(raw)
		require.NoError(t, err, "an envelope listing more than 32 bindings for one module is a valid ceiling")

		host := deliveredHost(t, writtenEnvelope{envelope}, ceilingReleaseKey, asking)
		require.NoError(t, host.modulePrincipals.RunOnce(context.Background()))
		grant := host.service.ModulePrincipals()[ModulePrincipalID(testModulePrefix)]
		require.Len(t, grant.OperationAudiences, count)
		for id, binding := range grant.OperationAudiences {
			require.Equal(t, DelegationModeModuleActor, binding.DelegationMode, id)
		}
		require.Empty(t, host.modulePrincipals.WithheldAuthorityReasons())
	})
}

// A grant whose full installation scope is larger than an installation scope
// may carry is refused, and the refusal names the module, how many bindings it
// holds, its size and the bound.
func TestAGrantNoInstallationScopeCanCarryIsRefusedByName(t *testing.T) {
	grantOf := func(count int) ModulePrincipalGrant {
		grant := ModulePrincipalGrant{Prefix: testModulePrefix, Workloads: []ModuleWorkload{testWorkload}, OperationAudiences: map[string]ModuleOperationAudience{}}
		scopes := make([]ModuleOperationScope, 0, 16)
		for k := range 16 {
			scopes = append(scopes, ModuleOperationScope{ResourceKind: fmt.Sprintf("kind%02d", k), Actions: []string{"read", "write"}})
		}
		for i := range count {
			grant.OperationAudiences[fmt.Sprintf("binding-%05d", i)] = ModuleOperationAudience{Audience: "producer", InvokeScopes: scopes}
		}
		return grant
	}
	one, err := json.Marshal(grantOf(1).OperationAudiences)
	require.NoError(t, err)
	// Each binding adds its encoding less the map's braces, plus a comma.
	past := installationScopeMaxBytes/(len(one)-1) + 1

	require.NoError(t, ValidateModulePrincipalGrant(testModulePrefix, grantOf(past/2)), "a grant within the bound is valid")

	grant := grantOf(past)
	document, err := json.Marshal(FullInstallationScope(grant))
	require.NoError(t, err)
	require.Greater(t, len(document), installationScopeMaxBytes)
	err = ValidateModulePrincipalGrant(testModulePrefix, grant)
	require.ErrorIs(t, err, ErrModuleGrantNotInstallable)
	require.Contains(t, err.Error(), fmt.Sprintf(`module principal %q holds %d operation bindings`, testModulePrefix, past))
	require.Contains(t, err.Error(), fmt.Sprintf("encodes to %d bytes, more than the %d bytes an installation scope may carry", len(document), installationScopeMaxBytes))
}

// The same refusal at delivery: a signed manifest within the manifest bound
// whose grant no installation scope can carry is omitted, and the reason —
// not only "the registry holds no entry" — is what the binding's row records
// and what a connect that needed one of its bindings is refused with.
func TestADeliveredGrantNoInstallationScopeCanCarryIsOmittedWithItsReason(t *testing.T) {
	// Short kinds asked for read-only spell compactly in a manifest and
	// expand in a grant, which is the gap this bound closes.
	const count = 900
	asking := testManifest(t, nil)
	for i := range count {
		operations := map[string][]string{}
		for k := range 16 {
			operations["invoke"] = append(operations["invoke"], fmt.Sprintf("k%02d:read", k))
			operations["lookup"] = append(operations["lookup"], fmt.Sprintf("k%02d:read", k))
		}
		asking.Authority.Bindings = append(asking.Authority.Bindings, solutionhost.AuthorityBinding{
			ID: fmt.Sprintf("B%04d", i), Audience: "producer", Operations: operations,
		})
	}
	require.NoError(t, asking.Validate())
	payload, err := solutionhost.Marshal(asking)
	require.NoError(t, err)
	require.LessOrEqual(t, len(payload), solutionhost.ManifestMaxBytes, "the manifest itself is deliverable")

	host := deliveredHost(t, noEnvelopeWritten{}, ceilingReleaseKey, asking)
	err = host.modulePrincipals.RunOnce(context.Background())
	require.ErrorIs(t, err, ErrModulePrincipalOmitted)
	require.ErrorIs(t, err, ErrModuleGrantNotInstallable)
	var omission ModulePrincipalOmission
	require.True(t, errors.As(err, &omission))
	require.Equal(t, testModulePrefix, omission.Prefix)
	require.Equal(t, testPresenceBinding, omission.Manifest)
	require.Empty(t, host.service.ModulePrincipals())

	reason := host.modulePrincipals.WithheldAuthorityReasons()[testPresenceBinding]
	require.Contains(t, reason, ErrModuleGrantNotInstallable.Error())
	require.Contains(t, reason, fmt.Sprintf("so all %d bindings it asks for are withheld", count))

	host.modulePrincipals.mutex.Lock()
	withheld := host.modulePrincipals.last.withheld
	host.modulePrincipals.mutex.Unlock()
	require.Len(t, withheld, count)
	require.ErrorIs(t, withheld[0], ErrModuleGrantNotInstallable,
		"each withheld binding carries the principal's reason, so a connect refused for one names it")
	require.Contains(t, withheld[0].Error(), "the registry holds no entry for this module principal")
}
