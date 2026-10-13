package business

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Availability is derived from delivered presence alone: a solution is active
// once its applied generation carries a frontend block, a module once its
// presence is applied, and nothing a running process reports moves either.
func TestSolutionRegistrationStatusIsDerivedFromDeliveredPresence(t *testing.T) {
	record := &SolutionRegistration{
		Declared: &SolutionDeclaredBinding{BindingID: "acme.test.example", Generation: 1, TargetID: "example-target", Kind: SolutionDeclaredKindSolution},
		Frontend: &SolutionFrontendHalf{Revision: 1, Manifest: `{"title":"Example","order":1,"exposed_module":"./Page","host_contract":1}`},
	}
	require.Equal(t, SolutionRegistrationActive, record.Status())
	record.Frontend = nil
	require.Equal(t, SolutionRegistrationPending, record.Status(), "a solution whose presence declares no frontend block has no page to render")
	record.Declared.Kind = SolutionDeclaredKindModule
	require.Equal(t, SolutionRegistrationActive, record.Status(), "a module fronts no page and waits for nothing")
	now := time.Now()
	record.TombstonedAt = &now
	require.Equal(t, SolutionRegistrationTombstoned, record.Status())
}
