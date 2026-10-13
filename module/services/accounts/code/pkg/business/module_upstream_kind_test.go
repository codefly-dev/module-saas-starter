package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// upstreamRecordStore holds one applied binding and its registration in memory
// and runs a control-plane transaction as a plain call.
type upstreamRecordStore struct {
	Store
	binding      *SolutionHostBindingRecord
	registration *SolutionRegistration
	saves        int
}

func (s *upstreamRecordStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *upstreamRecordStore) GetSolutionHostBindingForUpdate(context.Context, string) (*SolutionHostBindingRecord, error) {
	return s.binding, nil
}

func (s *upstreamRecordStore) GetSolutionRegistrationForUpdate(context.Context, string) (*SolutionRegistration, error) {
	copied := *s.registration
	return &copied, nil
}

func (s *upstreamRecordStore) NextSolutionRegistryRevision(context.Context) (int64, error) { return 2, nil }

func (s *upstreamRecordStore) SaveSolutionRegistration(_ context.Context, record *SolutionRegistration) error {
	s.saves++
	s.registration = record
	return nil
}

// A module's mint may not record where it serves: the gateway resolves a
// module's address through the Codefly SDK from what the composition wrote. A
// solution still reports its own, until it moves to the same resolution.
func TestAModuleMintRecordsNoAddress(t *testing.T) {
	for _, tc := range []struct {
		kind    SolutionDeclaredKind
		refused bool
	}{
		{SolutionDeclaredKindModule, true},
		{SolutionDeclaredKindSolution, false},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			store := &upstreamRecordStore{
				binding: &SolutionHostBindingRecord{BindingID: "acme.test.documents",
					Applied: &SolutionHostBindingApplied{SolutionID: "documents"}},
				registration: &SolutionRegistration{SolutionID: "documents", Revision: 1,
					Declared: &SolutionDeclaredBinding{BindingID: "acme.test.documents", Kind: tc.kind}},
			}
			service := &Service{store: store}
			principal := ModulePrincipalID("documents")
			snapshot := &installedModuleRegistry{registry: ModulePrincipalRegistry{
				principal: {Prefix: "documents", PresenceBinding: "acme.test.documents"},
			}}
			err := service.RecordModuleUpstream(context.Background(),
				ModuleWorkContextAuthority{PrincipalID: principal, snapshot: snapshot},
				"https://documents.documents.svc:8443", "")
			if tc.refused {
				require.ErrorIs(t, err, ErrModuleUpstreamRefused)
				require.Zero(t, store.saves)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 1, store.saves)
		})
	}
}
