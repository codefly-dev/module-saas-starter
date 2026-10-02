package business

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/codefly-dev/core/composition"
	"github.com/codefly-dev/core/solutionhost"
)

// Admission is exercised against core's own shipped fixtures rather than against
// documents written here. A document invented in this repository agrees with this
// repository's reading of the spec by construction, which is exactly the
// agreement that is worth nothing: the renderer in codefly-dev/cli and this host
// have to reach the same verdict on the same bytes, and the fixtures are those
// bytes.

// TestSolutionHostBindingFixturesReachTheirRequiredOutcome is the conformance
// test core ships the fixtures for: each one carries the outcome a conforming
// implementation must reach against solutionhost.FixtureHost, and this host must
// reach it through the same path it reconciles with.
func TestSolutionHostBindingFixturesReachTheirRequiredOutcome(t *testing.T) {
	host, err := solutionhost.FixtureHost()
	if err != nil {
		t.Fatalf("fixture host: %v", err)
	}
	// Core ships presence, authority and signed-carrier fixtures. This host's
	// admission path is the presence reconciler, so it is driven with the presence
	// set; the authority and carrier documents are verified before admission and
	// belong to the delivery endpoint's own conformance run.
	all := solutionhost.Fixtures()
	var fixtures []solutionhost.Fixture
	for _, fixture := range all {
		if fixture.Type == solutionhost.DocumentTypePresence {
			fixtures = append(fixtures, fixture)
		}
	}
	if len(fixtures) == 0 {
		t.Fatalf("core shipped no presence fixtures among %d, so nothing was actually checked", len(all))
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			document, parseErr := solutionhost.Parse(fixture.Document)
			if parseErr != nil {
				if fixture.Outcome != solutionhost.OutcomeRejected {
					t.Fatalf("fixture must be %s but did not parse: %v (%s)",
						fixture.Outcome, parseErr, fixture.Reason)
				}
				return
			}
			admission := admitSolutionHostBindings(host, []*solutionhost.SolutionHostBinding{document})
			switch fixture.Outcome {
			case solutionhost.OutcomeAccepted:
				if reason, withheld := admission.Withheld[document.Binding]; withheld {
					t.Fatalf("fixture must be accepted but was withheld: %s (%s)", reason, fixture.Reason)
				}
				if len(admission.Accepted) != 1 {
					t.Fatalf("accepted %d documents, want 1", len(admission.Accepted))
				}
				if got := admission.Accepted[0].Decision; got != fixture.Decision {
					t.Fatalf("decision = %q, want %q (%s)", got, fixture.Decision, fixture.Reason)
				}
			case solutionhost.OutcomeRejected:
				if len(admission.Accepted) != 0 {
					t.Fatalf("fixture must be rejected but was admitted (%s)", fixture.Reason)
				}
				if _, withheld := admission.Withheld[document.Binding]; !withheld {
					t.Fatalf("fixture was neither admitted nor attributed a reason (%s)", fixture.Reason)
				}
			default:
				t.Fatalf("unknown required outcome %q", fixture.Outcome)
			}
		})
	}
}

// The duplicate-route-alias fixture is rejected because an alias the fixture host
// already holds is claimed by a second binding. That is the one fixture whose
// verdict depends on host state rather than on the document, so it is worth
// pinning that the host reaches it for the collision reason and not by accident.
func TestSolutionHostBindingCollisionWithAnAppliedAliasNamesTheCollision(t *testing.T) {
	host, err := solutionhost.FixtureHost()
	if err != nil {
		t.Fatalf("fixture host: %v", err)
	}
	document := mustFixtureDocument(t, "duplicate-route-alias")
	admission := admitSolutionHostBindings(host, []*solutionhost.SolutionHostBinding{document})
	reason, withheld := admission.Withheld[document.Binding]
	if !withheld {
		t.Fatal("a binding claiming an alias an applied binding holds must be withheld")
	}
	if !strings.Contains(reason, composition.ErrCollision.Error()) {
		t.Fatalf("reason %q does not name the collision", reason)
	}
}

// A stale document must not hold back a sibling binding's new generation.
//
// This is the failure mode that makes per-document isolation load-bearing rather
// than a nicety: the renderer derives a generation from the previously delivered
// document, so a render from a checkout that cannot see the prior tree emits
// generation 1, which this host correctly refuses. If that refusal held the whole
// set, one bad pipeline run would freeze every other solution on the host.
func TestSolutionHostBindingStaleDocumentDoesNotBlockASibling(t *testing.T) {
	host, err := solutionhost.FixtureHost()
	if err != nil {
		t.Fatalf("fixture host: %v", err)
	}
	stale := mustFixtureDocument(t, "stale-generation")
	sibling := mustFixtureDocument(t, "valid")
	sibling.Binding = "pim-eu-west-1-01"
	sibling.Routes[0].Alias = "pim"

	admission := admitSolutionHostBindings(host, []*solutionhost.SolutionHostBinding{stale, sibling})

	if _, withheld := admission.Withheld[stale.Binding]; !withheld {
		t.Fatal("the stale document must be withheld")
	}
	if reason, withheld := admission.Withheld[sibling.Binding]; withheld {
		t.Fatalf("the sibling must still be admitted, was withheld: %s", reason)
	}
	if len(admission.Accepted) != 1 || admission.Accepted[0].Document.Binding != sibling.Binding {
		t.Fatalf("accepted = %v, want only %q", bindingsOf(admission.Accepted), sibling.Binding)
	}
}

// Two documents claiming one alias: exactly one is admitted, and which one does
// not depend on the order the mount was walked in.
//
// This asserts core's rule, not a rule of this host's. An earlier version of this
// host attributed collisions itself and withheld EVERY claimant, reasoning that it
// had no rule making one of two simultaneous claims the winner. core v0.7.0 has
// one — lowest binding ID, resolved one refusal at a time until the set is stable
// — and it is the better answer: refusing both wedges two solutions where
// admitting the deterministic winner keeps one serving. The host no longer owns
// this policy, so the test pins core's.
func TestSolutionHostBindingTwoClaimantsOfOneAliasYieldOneDeterministicWinner(t *testing.T) {
	host := solutionhost.Host{Coordinate: solutionhost.FixtureCoordinate, Domains: []string{solutionhost.FixtureDomain}}
	first := mustFixtureDocument(t, "valid")
	first.Binding = "crm-eu-west-1-aa"
	second := mustFixtureDocument(t, "valid")
	second.Binding = "crm-eu-west-1-bb"

	forward := admitSolutionHostBindings(host, []*solutionhost.SolutionHostBinding{first, second})
	reverse := admitSolutionHostBindings(host, []*solutionhost.SolutionHostBinding{second, first})

	for label, admission := range map[string]solutionHostBindingAdmission{"forward": forward, "reverse": reverse} {
		if len(admission.Accepted) != 1 {
			t.Fatalf("%s: accepted %v, want exactly one claimant", label, bindingsOf(admission.Accepted))
		}
		if got := admission.Accepted[0].Document.Binding; got != first.Binding {
			t.Fatalf("%s: admitted %q, want the lowest binding ID %q", label, got, first.Binding)
		}
		reason, withheld := admission.Withheld[second.Binding]
		if !withheld {
			t.Fatalf("%s: the losing claimant was neither admitted nor attributed a reason", label)
		}
		if !strings.Contains(reason, composition.ErrCollision.Error()) {
			t.Fatalf("%s: reason %q does not name the collision", label, reason)
		}
	}
}

// A collision must not take an unrelated binding down with it: the loser is
// refused, the winner and every unrelated binding still apply.
func TestSolutionHostBindingCollisionWithholdsOnlyTheLosingClaimant(t *testing.T) {
	host := solutionhost.Host{Coordinate: solutionhost.FixtureCoordinate, Domains: []string{solutionhost.FixtureDomain}}
	first := mustFixtureDocument(t, "valid")
	first.Binding = "crm-eu-west-1-aa"
	second := mustFixtureDocument(t, "valid")
	second.Binding = "crm-eu-west-1-bb"
	unrelated := mustFixtureDocument(t, "valid")
	unrelated.Binding = "pim-eu-west-1-01"
	unrelated.Routes[0].Alias = "pim"

	admission := admitSolutionHostBindings(host,
		[]*solutionhost.SolutionHostBinding{first, second, unrelated})

	admitted := bindingsOf(admission.Accepted)
	sort.Strings(admitted)
	if len(admitted) != 2 || admitted[0] != first.Binding || admitted[1] != unrelated.Binding {
		t.Fatalf("accepted = %v, want the winning claimant and the unrelated binding", admitted)
	}
	if _, withheld := admission.Withheld[second.Binding]; !withheld {
		t.Fatal("the losing claimant must be withheld")
	}
	if len(admission.Withheld) != 1 {
		t.Fatalf("withheld = %v, want only the losing claimant", admission.Withheld)
	}
}

// One binding delivered twice in one pass: the second occurrence is refused and
// names both documents, so an operator can find the duplicate mount. The first
// still applies — core resolves it rather than freezing the binding, and it
// reports which two documents collided.
func TestSolutionHostBindingDeclaredTwiceRefusesTheSecondOccurrence(t *testing.T) {
	host := solutionhost.Host{Coordinate: solutionhost.FixtureCoordinate, Domains: []string{solutionhost.FixtureDomain}}
	first := mustFixtureDocument(t, "valid")
	second := mustFixtureDocument(t, "valid")

	admission := admitSolutionHostBindings(host, []*solutionhost.SolutionHostBinding{first, second})

	if len(admission.Accepted) != 1 {
		t.Fatalf("accepted %v, want exactly one of the two", bindingsOf(admission.Accepted))
	}
	reason, withheld := admission.Withheld[first.Binding]
	if !withheld {
		t.Fatal("the duplicate must be attributed to its binding")
	}
	if !strings.Contains(reason, "declared twice") {
		t.Fatalf("reason %q does not say the binding was delivered twice", reason)
	}
}

// A document targeting another host is refused here, not merged. A host that
// reconciled whatever it was handed would reconcile its neighbour's desired
// state.
func TestSolutionHostBindingForAnotherCoordinateIsWithheld(t *testing.T) {
	// Derived from the fixtures' own coordinate rather than written out, so this
	// test names no deployment of its own.
	host := solutionhost.Host{Coordinate: solutionhost.FixtureCoordinate + "-elsewhere", Domains: []string{solutionhost.FixtureDomain}}
	document := mustFixtureDocument(t, "valid")

	admission := admitSolutionHostBindings(host, []*solutionhost.SolutionHostBinding{document})

	if len(admission.Accepted) != 0 {
		t.Fatalf("accepted %v, want nothing", bindingsOf(admission.Accepted))
	}
	if reason := admission.Withheld[document.Binding]; !strings.Contains(reason, solutionhost.ErrWrongHost.Error()) {
		t.Fatalf("reason %q does not name the wrong-host refusal", reason)
	}
}

// A generation whose contents changed under an applied generation number is the
// one thing core treats as an error rather than an answer, and the host must
// withhold it rather than reapply it.
func TestSolutionHostBindingRewrittenGenerationIsWithheld(t *testing.T) {
	host, err := solutionhost.FixtureHost()
	if err != nil {
		t.Fatalf("fixture host: %v", err)
	}
	rewritten := mustFixtureDocument(t, "valid")
	rewritten.Release.Version = "1.4.1"
	for index := range rewritten.Artifacts {
		rewritten.Artifacts[index].Release = rewritten.Release.Identity()
	}

	admission := admitSolutionHostBindings(host, []*solutionhost.SolutionHostBinding{rewritten})

	if len(admission.Accepted) != 0 {
		t.Fatalf("accepted %v, want nothing", bindingsOf(admission.Accepted))
	}
	if reason := admission.Withheld[rewritten.Binding]; !strings.Contains(reason, solutionhost.ErrRewrittenGeneration.Error()) {
		t.Fatalf("reason %q does not name the rewritten generation", reason)
	}
}

// An alias being handed over must not refuse its claimant.
//
// This is the case that makes per-document judging subtle: the binding holding the
// alias is IN the delivered set, as a tombstone releasing it, so its applied claim
// must not count against the claimant. Judging each document against every applied
// alias refused the claimant here, and the hand-over then never completed — the
// next pass read the same two documents and refused the same one, forever.
func TestSolutionHostBindingAliasHandedOverInOneSetAdmitsBoth(t *testing.T) {
	holder := mustFixtureDocument(t, "valid")
	applied, err := solutionhost.AppliedFrom(holder)
	if err != nil {
		t.Fatalf("applied from the holder: %v", err)
	}
	host := solutionhost.Host{
		Coordinate: solutionhost.FixtureCoordinate,
		Domains:    []string{solutionhost.FixtureDomain},
		Applied:    []solutionhost.Applied{applied},
	}
	releasing := mustFixtureDocument(t, "tombstone")
	claiming := mustFixtureDocument(t, "valid")
	claiming.Binding = "crm-eu-west-1-02"

	admission := admitSolutionHostBindings(host,
		[]*solutionhost.SolutionHostBinding{releasing, claiming})

	if len(admission.Withheld) != 0 {
		t.Fatalf("withheld %v, want a clean hand-over", admission.Withheld)
	}
	if len(admission.Accepted) != 2 {
		t.Fatalf("accepted = %v, want both sides of the hand-over", bindingsOf(admission.Accepted))
	}
}

// The other side of that rule: a candidate claiming an alias held by a binding
// whose document is NOT in the set is refused, and refused alone.
func TestSolutionHostBindingClaimingAHeldAliasWithholdsOnlyTheClaimant(t *testing.T) {
	holder := mustFixtureDocument(t, "valid")
	applied, err := solutionhost.AppliedFrom(holder)
	if err != nil {
		t.Fatalf("applied from the holder: %v", err)
	}
	host := solutionhost.Host{
		Coordinate: solutionhost.FixtureCoordinate,
		Domains:    []string{solutionhost.FixtureDomain},
		Applied:    []solutionhost.Applied{applied},
	}
	claiming := mustFixtureDocument(t, "valid")
	claiming.Binding = "crm-eu-west-1-02"
	unrelated := mustFixtureDocument(t, "valid")
	unrelated.Binding = "pim-eu-west-1-01"
	unrelated.Routes[0].Alias = "pim"

	admission := admitSolutionHostBindings(host,
		[]*solutionhost.SolutionHostBinding{claiming, unrelated})

	if _, withheld := admission.Withheld[claiming.Binding]; !withheld {
		t.Fatal("a candidate claiming a held alias must be withheld")
	}
	if len(admission.Accepted) != 1 || admission.Accepted[0].Document.Binding != unrelated.Binding {
		t.Fatalf("accepted = %v, want only the unrelated binding", bindingsOf(admission.Accepted))
	}
}

// A binding whose alias hands over to another binding is applied before the
// claimant, so the hand-over completes in one pass instead of waiting for the
// next one to find the key free.
func TestSolutionHostBindingAppliesReleasesBeforeClaims(t *testing.T) {
	releasing := mustFixtureDocument(t, "tombstone")
	claiming := mustFixtureDocument(t, "valid")
	claiming.Binding = "crm-eu-west-1-02"

	records := []*SolutionHostBindingRecord{
		{
			BindingID: releasing.Binding,
			Applied: &SolutionHostBindingApplied{
				SolutionHostBindingGeneration: SolutionHostBindingGeneration{Generation: 4},
				Routes:                        []string{"crm"},
				SolutionID:                    "crm",
			},
		},
	}
	// Deliberately given claimant-first, which is the order a mount walk would
	// produce for these two names.
	ordered := orderSolutionHostBindingApplies([]SolutionHostBindingDecision{
		{Document: claiming, Decision: solutionhost.DecisionApply},
		{Document: releasing, Decision: solutionhost.DecisionApply},
	}, records)

	if len(ordered) != 2 || ordered[0].Document.Binding != releasing.Binding {
		t.Fatalf("apply order = %v, want the releasing binding first", bindingsOf(ordered))
	}
}

// The registry key is the route alias, and an alias this host cannot address is
// refused with a reason naming the route rather than surfacing as a database
// error later.
func TestSolutionHostBindingRegistryKeyResolution(t *testing.T) {
	present := mustFixtureDocument(t, "valid")
	key, err := solutionHostBindingRegistryKey(present)
	if err != nil {
		t.Fatalf("valid fixture: %v", err)
	}
	if key != "crm" {
		t.Fatalf("registry key = %q, want the route alias %q", key, "crm")
	}

	dotted := mustFixtureDocument(t, "valid")
	dotted.Routes[0].Alias = "acme.crm"
	if _, err := solutionHostBindingRegistryKey(dotted); !errors.Is(err, ErrSolutionHostBindingRouteNotAddressable) {
		t.Fatalf("dotted alias error = %v, want ErrSolutionHostBindingRouteNotAddressable", err)
	}

	routeless := mustFixtureDocument(t, "valid")
	routeless.Routes = nil
	if _, err := solutionHostBindingRegistryKey(routeless); !errors.Is(err, ErrSolutionHostBindingRouteRequired) {
		t.Fatalf("routeless error = %v, want ErrSolutionHostBindingRouteRequired", err)
	}

	twoRoutes := mustFixtureDocument(t, "valid")
	twoRoutes.Routes = append(twoRoutes.Routes, solutionhost.Route{Alias: "crm-api", Surface: solutionhost.SurfaceBackend})
	if _, err := solutionHostBindingRegistryKey(twoRoutes); !errors.Is(err, ErrSolutionHostBindingRouteRequired) {
		t.Fatalf("two-route error = %v, want ErrSolutionHostBindingRouteRequired", err)
	}
}

// A document core refuses to parse still names the binding its refusal belongs
// to, so an operator reads "delivery is shipping something unreadable" beside the
// generation that is still running.
func TestSolutionHostBindingAttributionOfAnUnparseableDocument(t *testing.T) {
	_, problems := parseSolutionHostBindingDocuments([]SolutionHostBindingDocument{{
		Source: "mount/broken.codefly.yaml",
		Data: []byte("schema: codefly/solution-host-binding/v1\n" +
			"binding: crm-eu-west-1-01\n" +
			"generation: 4\n" +
			"whatIsThis: true\n"),
	}})
	if len(problems) != 1 {
		t.Fatalf("problems = %d, want 1", len(problems))
	}
	if problems[0].binding != "crm-eu-west-1-01" {
		t.Fatalf("attributed to %q, want the binding the document names", problems[0].binding)
	}
	if !strings.Contains(problems[0].err.Error(), "mount/broken.codefly.yaml") {
		t.Fatalf("problem %q does not name the document it came from", problems[0].err)
	}
}

// A document with no readable binding field is reported without a binding rather
// than attributed to a guess.
func TestSolutionHostBindingAttributionOfUnreadableBytes(t *testing.T) {
	_, problems := parseSolutionHostBindingDocuments([]SolutionHostBindingDocument{{
		Source: "mount/garbage.codefly.yaml",
		Data:   []byte("\x00\x01not yaml at all: [:"),
	}})
	if len(problems) != 1 {
		t.Fatalf("problems = %d, want 1", len(problems))
	}
	if problems[0].binding != "" {
		t.Fatalf("attributed to %q, want no attribution", problems[0].binding)
	}
}

// PendingGeneration is what an operator reads beside the reason: it is zero when
// desired and applied agree and the desired generation otherwise.
func TestSolutionHostBindingPendingGeneration(t *testing.T) {
	agreed := &SolutionHostBindingRecord{
		Desired: &SolutionHostBindingGeneration{Generation: 4, Digest: "sha256:aa"},
		Applied: &SolutionHostBindingApplied{
			SolutionHostBindingGeneration: SolutionHostBindingGeneration{Generation: 4, Digest: "sha256:aa"},
		},
	}
	if got := agreed.PendingGeneration(); got != 0 {
		t.Fatalf("pending generation = %d, want 0 when desired and applied agree", got)
	}
	behind := &SolutionHostBindingRecord{
		Desired: &SolutionHostBindingGeneration{Generation: 3, Digest: "sha256:bb"},
		Applied: &SolutionHostBindingApplied{
			SolutionHostBindingGeneration: SolutionHostBindingGeneration{Generation: 4, Digest: "sha256:aa"},
		},
	}
	if got := behind.PendingGeneration(); got != 3 {
		t.Fatalf("pending generation = %d, want the generation delivery is showing", got)
	}
	// The same generation number with different contents is pending, not current:
	// that is the rewrite core refuses, and reporting it as current would hide it.
	rewritten := &SolutionHostBindingRecord{
		Desired: &SolutionHostBindingGeneration{Generation: 4, Digest: "sha256:bb"},
		Applied: &SolutionHostBindingApplied{
			SolutionHostBindingGeneration: SolutionHostBindingGeneration{Generation: 4, Digest: "sha256:aa"},
		},
	}
	if got := rewritten.PendingGeneration(); got != 4 {
		t.Fatalf("pending generation = %d, want the rewritten generation", got)
	}
}

func mustFixtureDocument(t *testing.T, name string) *solutionhost.SolutionHostBinding {
	t.Helper()
	data, err := solutionhost.FixtureDocument(solutionhost.DocumentTypePresence, name)
	if err != nil {
		t.Fatalf("fixture %q: %v", name, err)
	}
	document, err := solutionhost.Parse(data)
	if err != nil {
		t.Fatalf("parse fixture %q: %v", name, err)
	}
	return document
}

func bindingsOf(decisions []SolutionHostBindingDecision) []string {
	names := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		names = append(names, decision.Document.Binding)
	}
	return names
}
