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

// countingDeclarationStore is declarationProjectionStore plus the audience
// vocabulary's read, counted.
type countingDeclarationStore struct {
	declarationProjectionStore
	reads int
}

func (s *countingDeclarationStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *countingDeclarationStore) LiveDeclaredSolutionBindingIDs(context.Context) ([]string, error) {
	s.reads++
	return nil, nil
}

// THE APPLY PATH INVALIDATES THE VOCABULARY SNAPSHOT — asserted by driving the
// apply, not by calling the invalidator.
//
// The first version of this test called Service.InvalidateHostAudiences() itself
// and passed with the invalidation deleted from both apply paths: it proved the
// mechanism worked and nothing about whether the reconciler used it. A fake for the
// thing under test proves nothing about the thing under test.
//
// Both directions, because they are different facts and only one of them is
// dangerous: a declaration ADDS an audience (a stale snapshot merely delays a new
// solution becoming addressable), and a withdrawal REMOVES one (a stale snapshot
// keeps a withdrawn solution addressable for up to the bound).
func TestTheApplyPathInvalidatesTheAudienceSnapshot(t *testing.T) {
	document := func(kind solutionhost.Kind) *solutionhost.SolutionHostBinding {
		return &solutionhost.SolutionHostBinding{
			Binding: "acme.test.example", Generation: 2, Kind: kind,
			Release: solutionhost.Release{Publisher: "acme", Name: "example", Version: "1.0.0"},
		}
	}

	t.Run("declaring invalidates", func(t *testing.T) {
		store := &countingDeclarationStore{}
		svc := &Service{store: store}
		_, err := svc.HostAudiences(context.Background())
		require.NoError(t, err)
		require.Equal(t, 1, store.reads)
		_, err = svc.HostAudiences(context.Background())
		require.NoError(t, err)
		require.Equal(t, 1, store.reads, "the snapshot serves inside the bound")

		_, err = svc.declareSolutionRegistration(
			context.Background(), document(solutionhost.KindSolution), "example", "target", time.Now())
		require.NoError(t, err)

		_, err = svc.HostAudiences(context.Background())
		require.NoError(t, err)
		require.Equal(t, 2, store.reads, "applying a declaration must drop the snapshot")
	})

	t.Run("withdrawing invalidates", func(t *testing.T) {
		store := &countingDeclarationStore{}
		store.current = &SolutionRegistration{
			SolutionID: "example", Publisher: "solution:example",
			Declared: &SolutionDeclaredBinding{BindingID: "acme.test.example", TargetID: "target", Kind: SolutionDeclaredKindSolution},
		}
		// emitTx returns nil when no audit emitter is wired, so the withdrawal's own
		// audit write is not what this test is about.
		svc := &Service{store: store}
		_, err := svc.HostAudiences(context.Background())
		require.NoError(t, err)
		require.Equal(t, 1, store.reads)

		record := &SolutionHostBindingRecord{
			BindingID: "acme.test.example",
			Applied:   &SolutionHostBindingApplied{SolutionID: "example"},
		}
		_, err = svc.withdrawDeclaredSolutionRegistration(
			context.Background(), record, document(solutionhost.KindSolution), "target", time.Now())
		require.NoError(t, err)

		_, err = svc.HostAudiences(context.Background())
		require.NoError(t, err)
		require.Equal(t, 2, store.reads,
			"applying a withdrawal must drop the snapshot: a stale one keeps a withdrawn solution addressable")
	})
}
