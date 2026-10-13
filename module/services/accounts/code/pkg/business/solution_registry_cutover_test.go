package business

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Availability is derived from delivered presence alone, for either kind and
// with or without a frontend. Installation readiness is a separate fact.
func TestSolutionRegistrationStatusIsDerivedFromDeliveredPresence(t *testing.T) {
	record := &SolutionRegistration{
		Declared: &SolutionDeclaredBinding{BindingID: "acme.test.example", Generation: 1, TargetID: "example-target", Kind: SolutionDeclaredKindSolution},
		Frontend: &SolutionFrontendHalf{Revision: 1, Manifest: `{"title":"Example","order":1,"exposed_module":"./Page","host_contract":1}`},
	}
	require.Equal(t, SolutionRegistrationActive, record.Status())
	record.Frontend = nil
	require.Equal(t, SolutionRegistrationActive, record.Status(), "a headless solution can serve its API")
	record.Declared.Kind = SolutionDeclaredKindModule
	require.Equal(t, SolutionRegistrationActive, record.Status(), "a headless module can serve its API")
	record.Declared = nil
	require.Equal(t, SolutionRegistrationPending, record.Status(), "a frontend alone cannot activate a record")
	now := time.Now()
	record.TombstonedAt = &now
	require.Equal(t, SolutionRegistrationTombstoned, record.Status())
}
