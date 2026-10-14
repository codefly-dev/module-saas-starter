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

// The frontend half is the applied generation's own frontend block and nothing
// older: a generation that declares none takes the page with it, whatever the
// record held before, and one that declares a block writes it whole.
func TestDeclaredPresenceWritesItsOwnFrontendBlock(t *testing.T) {
	frontend := &solutionhost.Frontend{Title: "Example", Order: 2, ExposedModule: "./Page", HostContract: 1,
		Surfaces: []solutionhost.FrontendSurface{{ID: "footnote", Client: "word", Title: "Footnote", Module: "/surfaces/footnote.js", Contract: 1, Applies: "always"}}}
	for _, tc := range []struct {
		name     string
		declared *SolutionDeclaredBinding
		frontend *solutionhost.Frontend
		want     string
	}{
		{"runtime, no block", nil, nil, ""},
		{"replaced target, no block", &SolutionDeclaredBinding{BindingID: "acme.test.example", TargetID: "old-target"}, nil, ""},
		{"same target, no block", &SolutionDeclaredBinding{BindingID: "acme.test.example", TargetID: "current-target"}, nil, ""},
		{"same target, a block", &SolutionDeclaredBinding{BindingID: "acme.test.example", TargetID: "current-target"}, frontend,
			`{"title":"Example","order":2,"exposed_module":"./Page","host_contract":1,"surfaces":[{"id":"footnote","client":"word","title":"Footnote","module":"/surfaces/footnote.js","contract":1,"applies":"always"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &declarationProjectionStore{current: &SolutionRegistration{
				SolutionID: "example", Publisher: "solution:example", Declared: tc.declared,
				Frontend: &SolutionFrontendHalf{Manifest: "{}"},
			}}
			svc := &Service{store: store}
			document := &solutionhost.SolutionHostBinding{Binding: "acme.test.example", Generation: 2,
				Kind:     solutionhost.KindSolution,
				Release:  solutionhost.Release{Publisher: "acme", Name: "example", Version: "1.0.0"},
				Frontend: tc.frontend}
			_, err := svc.declareSolutionRegistration(context.Background(), document, "example", "current-target", time.Now())
			require.NoError(t, err)
			require.NotNil(t, store.saved)
			if tc.want == "" {
				require.Nil(t, store.saved.Frontend)
			} else {
				require.NotNil(t, store.saved.Frontend)
				require.JSONEq(t, tc.want, store.saved.Frontend.Manifest)
				require.Equal(t, int64(10), store.saved.Frontend.Revision)
			}
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

// Presentation changes replace only the declaration's projection. A headless
// solution keeps the same installation target when its group moves or clears.
func TestDeclaredSolutionGroupFollowsAppliedGenerationWithoutChangingTarget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group *solutionhost.SolutionGroup
		want  *SolutionGroup
	}{
		{"rename and reorder", &solutionhost.SolutionGroup{ID: "operations", Title: "Operations tools", Order: -2}, &SolutionGroup{ID: "operations", Title: "Operations tools", Order: -2}},
		{"move", &solutionhost.SolutionGroup{ID: "research", Title: "Research", Order: 4}, &SolutionGroup{ID: "research", Title: "Research", Order: 4}},
		{"clear", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &declarationProjectionStore{current: &SolutionRegistration{
				SolutionID: "example", Publisher: "solution:example",
				Declared: &SolutionDeclaredBinding{BindingID: "acme.test.example", TargetID: "stable-target", Generation: 1, Kind: SolutionDeclaredKindSolution},
				Group:    &SolutionGroup{ID: "operations", Title: "Operations", Order: 1},
			}}
			svc := &Service{store: store}
			document := &solutionhost.SolutionHostBinding{
				Binding: "acme.test.example", Generation: 2, Kind: solutionhost.KindSolution,
				Release: solutionhost.Release{Publisher: "acme", Name: "example", Version: "1.0.0"}, Group: tc.group,
			}
			_, err := svc.declareSolutionRegistration(context.Background(), document, "example", "stable-target", time.Now())
			require.NoError(t, err)
			require.Equal(t, tc.want, store.saved.Group)
			require.Nil(t, store.saved.Frontend, "headless solutions are grouped without inventing a page")
			require.Equal(t, "stable-target", store.saved.Declared.TargetID)
			require.Equal(t, "example", store.saved.SolutionID)
			require.Equal(t, uint64(2), store.saved.Declared.Generation)
		})
	}
}

func TestWithdrawalClearsSolutionGroup(t *testing.T) {
	store := &declarationProjectionStore{current: &SolutionRegistration{
		SolutionID: "example", Publisher: "solution:example",
		Declared: &SolutionDeclaredBinding{BindingID: "acme.test.example", TargetID: "target", Kind: SolutionDeclaredKindSolution},
		Group:    &SolutionGroup{ID: "operations", Title: "Operations", Order: 1},
	}}
	svc := &Service{store: store}
	now := time.Now()
	_, err := svc.withdrawDeclaredSolutionRegistration(context.Background(),
		&SolutionHostBindingRecord{BindingID: "acme.test.example", Applied: &SolutionHostBindingApplied{SolutionID: "example"}},
		&solutionhost.SolutionHostBinding{Binding: "acme.test.example", Generation: 2, Kind: solutionhost.KindSolution, Removed: true,
			Release: solutionhost.Release{Publisher: "acme", Name: "example", Version: "1.0.0"}}, "target", now)
	require.NoError(t, err)
	require.Nil(t, store.saved.Group)
	require.Equal(t, &now, store.saved.TombstonedAt)
	require.Equal(t, "target", store.saved.Declared.TargetID)
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

func (s *countingDeclarationStore) LiveDeclaredBindings(context.Context) ([]LiveDeclaredBinding, error) {
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
