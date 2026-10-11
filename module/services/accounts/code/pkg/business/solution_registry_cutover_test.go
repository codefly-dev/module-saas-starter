package business

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSolutionRegistrationDoesNotExpireWithoutHeartbeat(t *testing.T) {
	record := &SolutionRegistration{
		Declared: &SolutionDeclaredBinding{BindingID: "acme.test.example", Generation: 1, TargetID: "example-target"},
		Frontend: &SolutionFrontendHalf{Manifest: `{"id":"example"}`, ContractVersion: "v1"},
		Backend:  &SolutionBackendHalf{Upstream: "http://example.svc", ServiceAlias: "example", ContractVersion: "v1"},
	}
	require.Equal(t, SolutionRegistrationActive, record.Status())
	record.Backend.ContractVersion = "v2"
	require.Equal(t, SolutionRegistrationIncompatible, record.Status())
	record.Backend = nil
	require.Equal(t, SolutionRegistrationPending, record.Status())
	now := time.Now()
	record.TombstonedAt = &now
	require.Equal(t, SolutionRegistrationTombstoned, record.Status())
}
