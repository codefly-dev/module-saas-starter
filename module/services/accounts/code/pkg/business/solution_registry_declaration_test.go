package business

import (
	"context"
	"testing"
	"time"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

type declarationProjectionStore struct {
	Store
	current *SolutionRegistration
	saved   *SolutionRegistration
}

func (s *declarationProjectionStore) GetSolutionRegistrationForUpdate(context.Context, string) (*SolutionRegistration, error) {
	return s.current, nil
}
func (s *declarationProjectionStore) NextSolutionRegistryRevision(context.Context) (int64, error) {
	return 10, nil
}
func (s *declarationProjectionStore) SaveSolutionRegistration(_ context.Context, record *SolutionRegistration) error {
	s.saved = record
	return nil
}

func TestDeclaredPresenceDoesNotAdoptRuntimeObservations(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared *SolutionDeclaredBinding
		keep     bool
	}{
		{"runtime", nil, false},
		{"replaced target", &SolutionDeclaredBinding{BindingID: "acme.test.example", TargetID: "old-target"}, false},
		{"same target", &SolutionDeclaredBinding{BindingID: "acme.test.example", TargetID: "current-target"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &declarationProjectionStore{current: &SolutionRegistration{
				SolutionID: "example", Publisher: "solution:example", Declared: tc.declared,
				Frontend: &SolutionFrontendHalf{Manifest: "{}"}, Backend: &SolutionBackendHalf{Upstream: "http://example.svc"},
			}}
			svc := &Service{store: store}
			document := &solutionhost.SolutionHostBinding{Binding: "acme.test.example", Generation: 2,
				Release: solutionhost.Release{Publisher: "acme", Name: "example", Version: "1.0.0"}}
			_, err := svc.declareSolutionRegistration(context.Background(), document, "example", "current-target", time.Now())
			require.NoError(t, err)
			require.NotNil(t, store.saved)
			require.Equal(t, tc.keep, store.saved.Frontend != nil)
			require.Equal(t, tc.keep, store.saved.Backend != nil)
			require.Equal(t, "current-target", store.saved.Declared.TargetID)
		})
	}
}
