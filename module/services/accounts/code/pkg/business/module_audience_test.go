package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// liveDeclarationsStore serves the live declarations the audience vocabulary is
// derived from, and runs a control-plane transaction as a plain call.
type liveDeclarationsStore struct {
	Store
	bindings []LiveDeclaredBinding
}

func (s *liveDeclarationsStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *liveDeclarationsStore) LiveDeclaredBindings(context.Context) ([]LiveDeclaredBinding, error) {
	return s.bindings, nil
}

// A live declared module's alias is an audience, so a solution can mint a
// viewer's Work Context for a module that only serves; a solution is addressed
// by its binding and never by its alias.
func TestALiveDeclaredModuleAliasIsAnAudience(t *testing.T) {
	service := &Service{store: &liveDeclarationsStore{bindings: []LiveDeclaredBinding{
		{BindingID: "acme.test.documents", Kind: SolutionDeclaredKindModule, Alias: "documents"},
		{BindingID: "acme.test.notebook", Kind: SolutionDeclaredKindSolution, Alias: "notebook"},
	}}}
	audiences, err := service.HostAudiences(context.Background())
	require.NoError(t, err)
	require.Contains(t, audiences, "documents")
	require.Contains(t, audiences, SolutionAudience("acme.test.documents"))
	require.Contains(t, audiences, SolutionAudience("acme.test.notebook"))
	require.NotContains(t, audiences, "notebook", "a solution is addressed by its binding, never its alias")
	require.Contains(t, audiences, DirectoryAudience)
	require.Len(t, audiences, 6, "module-capabilities, source operations, directory, two bindings and one module alias")
	require.NoError(t, service.RequireHostAudience(context.Background(), "documents"))
	require.ErrorIs(t, service.RequireHostAudience(context.Background(), "notebook"), ErrAudienceNotInHostVocabulary)
}

// A declared module is active once its presence is applied: its address is
// resolved by the gateway, never reported, so no half is waited for.
func TestADeclaredModuleIsActiveWithNoReportedHalf(t *testing.T) {
	module := &SolutionRegistration{SolutionID: "documents", Declared: &SolutionDeclaredBinding{BindingID: "acme.test.documents", Kind: SolutionDeclaredKindModule}}
	require.Equal(t, SolutionRegistrationActive, module.Status())
	solution := &SolutionRegistration{SolutionID: "notebook", Declared: &SolutionDeclaredBinding{BindingID: "acme.test.notebook", Kind: SolutionDeclaredKindSolution}}
	require.Equal(t, SolutionRegistrationActive, solution.Status(), "a headless solution is available independently of frontend metadata")
}
