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
				Kind:    solutionhost.KindSolution,
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

// The KIND the document declared reaches the registry record, and it is the
// document's own value rather than a default (issue #952).
//
// The host used to drop it, which left the registry unable to say whether a
// record was a solution or a module — and the two are routed on surfaces whose
// admission differs, so a gateway that could not tell them apart had to serve
// both from one surface or serve neither.
//
// Asserted for BOTH kinds in one table, because a check that only ever saw
// "solution" would pass against a writer that hardcoded it.
func TestTheDeclaredKindReachesTheRegistryRecord(t *testing.T) {
	for _, tc := range []struct {
		kind solutionhost.Kind
		want SolutionDeclaredKind
	}{
		{solutionhost.KindSolution, SolutionDeclaredKindSolution},
		{solutionhost.KindModule, SolutionDeclaredKindModule},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			store := &declarationProjectionStore{}
			svc := &Service{store: store}
			document := &solutionhost.SolutionHostBinding{
				Binding: "acme.test.example", Generation: 2, Kind: tc.kind,
				Release: solutionhost.Release{Publisher: "acme", Name: "example", Version: "1.0.0"},
			}
			_, err := svc.declareSolutionRegistration(
				context.Background(), document, "example", "current-target", time.Now())
			require.NoError(t, err)
			require.NotNil(t, store.saved)
			require.Equal(t, tc.want, store.saved.Declared.Kind)
		})
	}
}

// A kind this host has no routing surface for is REFUSED, and the refusal leaves
// the record alone.
//
// Core admits only its own two values, so this is reachable only from a core whose
// vocabulary grew past this host's. Writing the record anyway would produce a row
// no surface serves and no operator can explain; refusing names the binding and
// leaves whatever generation was running in place.
//
// The empty kind is in the table deliberately: it is what a document carries when
// a caller built it by hand, and it must not pass as "the default kind".
func TestADeclaredKindThisHostDoesNotRouteIsRefused(t *testing.T) {
	for _, kind := range []solutionhost.Kind{"", "gadget", "SOLUTION"} {
		t.Run(string("kind="+kind), func(t *testing.T) {
			store := &declarationProjectionStore{}
			svc := &Service{store: store}
			document := &solutionhost.SolutionHostBinding{
				Binding: "acme.test.example", Generation: 2, Kind: kind,
				Release: solutionhost.Release{Publisher: "acme", Name: "example", Version: "1.0.0"},
			}
			_, err := svc.declareSolutionRegistration(
				context.Background(), document, "example", "current-target", time.Now())
			require.ErrorIs(t, err, ErrSolutionHostBindingKindNotRoutable)
			require.Contains(t, err.Error(), "acme.test.example",
				"the refusal must name the binding an operator has to go and look at")
			require.Nil(t, store.saved, "nothing may be written for a kind no surface serves")
		})
	}
}
