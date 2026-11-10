package business

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/memstore"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
)

// solutionInstallationSeals answers the organization's installation of each
// solution binding it knows, and that no other is installed.
type solutionInstallationSeals struct {
	WorkContextSealStore
	installed map[string]string
}

func (s solutionInstallationSeals) WorkContextSolutionInstallation(_ context.Context, _, bindingID string) (string, error) {
	if id, ok := s.installed[bindingID]; ok {
		return id, nil
	}
	return "", ErrWorkContextNoAudienceInstallation
}

// everyPrincipalInstalled is a live module authority that answers every
// principal installed, as a composition installs a module: what a solution,
// installed explicitly, must never be resolved through.
type everyPrincipalInstalled struct{ ModuleAuthorityStore }

func (everyPrincipalInstalled) LiveModuleAuthority(_ context.Context, principalID, orgID, _ string) (*LiveModuleAuthority, error) {
	return &LiveModuleAuthority{InstallationID: orgID + "/" + principalID}, nil
}

// A delivered solution named by its alias resolves to the organization's real
// installation of its binding, or to none, never to a composed module's
// installation it does not have.
func TestADeliveredSolutionAliasResolvesToItsOwnInstallation(t *testing.T) {
	const org = "00000000-0000-7000-8000-0000000000c1"
	svc, err := NewService(&liveDeclarationsStore{bindings: []LiveDeclaredBinding{
		{BindingID: "acme.test.pages", Kind: SolutionDeclaredKindSolution, Alias: "pages"},
		{BindingID: "acme.test.records", Kind: SolutionDeclaredKindModule, Alias: "records"},
		{BindingID: "acme.test.idle", Kind: SolutionDeclaredKindSolution, Alias: "idle"},
	}})
	require.NoError(t, err)
	svc.SetModulePrincipals(ModulePrincipalRegistry{
		ModulePrincipalID("pages"):   {Prefix: "pages", PresenceBinding: "acme.test.pages"},
		ModulePrincipalID("records"): {Prefix: "records", PresenceBinding: "acme.test.records"},
		ModulePrincipalID("idle"):    {Prefix: "idle", PresenceBinding: "acme.test.idle"},
	})
	svc.SetModuleAuthorityReads(everyPrincipalInstalled{}, nil)
	svc.SetWorkContextSealStore(solutionInstallationSeals{installed: map[string]string{"acme.test.pages": "0199a000-0000-7000-8000-0000000000a1"}})
	// The authority core composes no module page here, so a module audience
	// resolves to the module's own installation.
	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	core, err := authority.New(memstore.New(), authority.Config{Issuer: "accounts.test", KeyID: "alias-test", Signer: key,
		Keys: staticKeys{"alias-test": key.Public().(ed25519.PublicKey)}, Replay: workcontext.NewMemoryReplayStore(), HostInstallsModules: true})
	require.NoError(t, err)
	svc.SetAuthorityCoreForTest(core)

	installation, err := svc.WorkContextAudienceInstallation(context.Background(), org, "pages")
	require.NoError(t, err)
	require.Equal(t, "0199a000-0000-7000-8000-0000000000a1", installation)

	_, err = svc.WorkContextAudienceInstallation(context.Background(), org, "idle")
	require.ErrorIs(t, err, ErrWorkContextNoAudienceInstallation, "a solution never installed has no installation, whatever the composition answers")

	installation, err = svc.WorkContextAudienceInstallation(context.Background(), org, "records")
	require.NoError(t, err)
	require.Equal(t, org+"/"+ModulePrincipalID("records"), installation, "a module is installed by composition")
}
