package business

import (
	"bytes"
	"context"
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
			admission := admitSolutionHostBindings(host, delivered(t, document))
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
	admission := admitSolutionHostBindings(host, delivered(t, document))
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

	admission := admitSolutionHostBindings(host, delivered(t, stale, sibling))

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
	host := fixtureSignerHost(solutionhost.FixtureCoordinate)
	first := mustFixtureDocument(t, "valid")
	first.Binding = "crm-eu-west-1-aa"
	second := mustFixtureDocument(t, "valid")
	second.Binding = "crm-eu-west-1-bb"

	forward := admitSolutionHostBindings(host, delivered(t, first, second))
	reverse := admitSolutionHostBindings(host, delivered(t, second, first))

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
	host := fixtureSignerHost(solutionhost.FixtureCoordinate)
	first := mustFixtureDocument(t, "valid")
	first.Binding = "crm-eu-west-1-aa"
	second := mustFixtureDocument(t, "valid")
	second.Binding = "crm-eu-west-1-bb"
	unrelated := mustFixtureDocument(t, "valid")
	unrelated.Binding = "pim-eu-west-1-01"
	unrelated.Routes[0].Alias = "pim"

	admission := admitSolutionHostBindings(host,
		delivered(t, first, second, unrelated))

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
	host := fixtureSignerHost(solutionhost.FixtureCoordinate)
	first := mustFixtureDocument(t, "valid")
	second := mustFixtureDocument(t, "valid")

	admission := admitSolutionHostBindings(host, delivered(t, first, second))

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
	host := fixtureSignerHost(solutionhost.FixtureCoordinate + "-elsewhere")
	document := mustFixtureDocument(t, "valid")

	admission := admitSolutionHostBindings(host, delivered(t, document))

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

	admission := admitSolutionHostBindings(host, delivered(t, rewritten))

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
	host := fixtureSignerHost(solutionhost.FixtureCoordinate)
	host.Applied = []solutionhost.Applied{applied}
	releasing := mustFixtureDocument(t, "tombstone")
	claiming := mustFixtureDocument(t, "valid")
	claiming.Binding = "crm-eu-west-1-02"

	admission := admitSolutionHostBindings(host,
		delivered(t, releasing, claiming))

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
	host := fixtureSignerHost(solutionhost.FixtureCoordinate)
	host.Applied = []solutionhost.Applied{applied}
	claiming := mustFixtureDocument(t, "valid")
	claiming.Binding = "crm-eu-west-1-02"
	unrelated := mustFixtureDocument(t, "valid")
	unrelated.Binding = "pim-eu-west-1-01"
	unrelated.Routes[0].Alias = "pim"

	admission := admitSolutionHostBindings(host,
		delivered(t, claiming, unrelated))

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
	// Read from the fixture rather than hardcoded: core moved this alias once
	// already, and pinning its content here tests core's testdata rather than
	// this host's resolution.
	if key != present.Routes[0].Alias {
		t.Fatalf("registry key = %q, want the route alias %q", key, present.Routes[0].Alias)
	}
	// The fixture's route alias and its ownership domain are currently the SAME
	// string, so the assertion above cannot tell "the alias" from "the domain"
	// — and the domain is the plausible wrong answer. Re-asking with a distinct
	// alias is what actually pins which field is the registry key.
	distinct := mustFixtureDocument(t, "valid")
	distinct.Routes[0].Alias = "storefront"
	if key, err = solutionHostBindingRegistryKey(distinct); err != nil {
		t.Fatalf("distinct alias: %v", err)
	}
	if key != "storefront" {
		t.Fatalf("registry key = %q, want the route alias %q and not the ownership domain %q",
			key, "storefront", distinct.OwnershipDomain)
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

// A verified payload core refuses to parse still names the binding its refusal
// belongs to, so an operator reads "delivery is shipping something unreadable"
// beside the generation that is still running.
func TestSolutionHostBindingAttributionOfAnUnparseableDocument(t *testing.T) {
	_, problems := verifySolutionHostBindingDocuments(context.Background(), fixtureSigner(),
		[]SolutionHostBindingDocument{{
			Source: "mount/broken.codefly.yaml",
			Data: carrierOver(t, []byte(`{"schema":"codefly/solution-host-binding/v1",`+
				`"binding":"crm-eu-west-1-01","generation":4,"whatIsThis":true}`)),
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

// A payload with no readable binding field is reported without a binding rather
// than attributed to a guess.
func TestSolutionHostBindingAttributionOfUnreadableBytes(t *testing.T) {
	_, problems := verifySolutionHostBindingDocuments(context.Background(), fixtureSigner(),
		[]SolutionHostBindingDocument{{
			Source: "mount/garbage.codefly.yaml",
			// A JSON value that is not a document at all. The carrier itself is
			// JSON, so bytes that are not even a JSON value are refused one
			// layer earlier, by ParseSigned — which is what
			// TestSolutionHostBindingRefusesAnUnsignedDocument covers.
			Data: carrierOver(t, []byte(`"not a document"`)),
		}})
	if len(problems) != 1 {
		t.Fatalf("problems = %d, want 1", len(problems))
	}
	if problems[0].binding != "" {
		t.Fatalf("attributed to %q, want no attribution", problems[0].binding)
	}
}

// The regression that matters most on this change: a BARE document — the shape
// delivery shipped before signing existed, and the shape every fixture file on
// disk still has — is refused rather than admitted. There is no path from
// unattested bytes to a host judgement, and this is the test that says so.
func TestSolutionHostBindingRefusesAnUnsignedDocument(t *testing.T) {
	bare, err := solutionhost.Marshal(mustFixtureDocument(t, "valid"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	verified, problems := verifySolutionHostBindingDocuments(context.Background(), fixtureSigner(),
		[]SolutionHostBindingDocument{{Source: "mount/unsigned.codefly.yaml", Data: bare}})
	if len(verified) != 0 {
		t.Fatalf("verified %d unsigned documents, want none admitted without an attestation", len(verified))
	}
	if len(problems) != 1 {
		t.Fatalf("problems = %d, want 1", len(problems))
	}
	// A bare document is not a carrier, so there is nothing whose binding could
	// be trusted enough to attribute: core refuses it before any field is read.
	if problems[0].binding != "" {
		t.Fatalf("attributed to %q, want no attribution for bytes that are not a carrier", problems[0].binding)
	}
	if !errors.Is(problems[0].err, solutionhost.ErrUnsigned) {
		t.Fatalf("error = %v, want ErrUnsigned", problems[0].err)
	}
}

// A carrier whose bundle does not verify is refused, and the refusal is still
// attributed so an operator sees which binding delivery is failing for.
func TestSolutionHostBindingRefusesACarrierWhoseBundleDoesNotVerify(t *testing.T) {
	document := mustFixtureDocument(t, "valid")
	refusing := &stubBundleVerifier{err: errors.New("no identity in the allowlist matched")}
	verified, problems := verifySolutionHostBindingDocuments(context.Background(), refusing,
		[]SolutionHostBindingDocument{{
			Source: "mount/forged.codefly.yaml",
			Data:   signedCarrier(t, document),
		}})
	if len(verified) != 0 {
		t.Fatalf("verified %d documents, want none", len(verified))
	}
	if len(problems) != 1 {
		t.Fatalf("problems = %d, want 1", len(problems))
	}
	if problems[0].binding != document.Binding {
		t.Fatalf("attributed to %q, want %q", problems[0].binding, document.Binding)
	}
}

// The verifier must be asked about the carrier's bytes VERBATIM. Re-encoding a
// parsed document would produce bytes the signature does not cover, and then the
// only honest answer is that nothing verified.
func TestSolutionHostBindingVerifierSeesTheCarrierBytesVerbatim(t *testing.T) {
	document := mustFixtureDocument(t, "valid")
	canonical, err := document.CanonicalBytes()
	if err != nil {
		t.Fatalf("canonical bytes: %v", err)
	}
	verifier := fixtureSigner()
	if _, problems := verifySolutionHostBindingDocuments(context.Background(), verifier,
		[]SolutionHostBindingDocument{{Source: "mount/valid.codefly.yaml", Data: carrierOver(t, canonical)}},
	); len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}
	if len(verifier.payloads) != 1 {
		t.Fatalf("verifier asked %d times, want 1", len(verifier.payloads))
	}
	if !bytes.Equal(verifier.payloads[0], canonical) {
		t.Fatal("the verifier was asked about bytes other than the carrier's document")
	}
}

// A signer the host accepts at all must still not be able to deliver under a
// domain the host did not let it speak for. This is the self-asserted ownership
// domain hole: the document states its own domain, so the signer policy is the
// only thing standing between an accepted signer and every accepted domain.
func TestSolutionHostBindingRefusesADomainTheSignerMayNotSpeakFor(t *testing.T) {
	host, err := solutionhost.FixtureHost()
	if err != nil {
		t.Fatalf("fixture host: %v", err)
	}
	document := mustFixtureDocument(t, "valid")
	// An identity the host's policy says nothing about, attesting a document
	// whose domain the host does accept.
	stranger := &stubBundleVerifier{signer: "https://signer.example/other-workflow@refs/heads/main"}
	admission := admitSolutionHostBindings(host, deliveredBy(t, stranger, document))
	if len(admission.Accepted) != 0 {
		t.Fatalf("accepted %v, want nothing from a signer with no domain policy", bindingsOf(admission.Accepted))
	}
	reason, withheld := admission.Withheld[document.Binding]
	if !withheld {
		t.Fatal("a document delivered by a signer the host does not let speak for its domain must be withheld")
	}
	if !strings.Contains(reason, "speak for it") {
		t.Fatalf("reason %q does not say the signer may not speak for the domain", reason)
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

// Admission must read the generation the attestation covered, not the one a
// caller holding the document happens to have moved.
//
// This pins a property of the DEPENDENCY rather than of local logic, which is
// why it is worth a test here: core's Delivered used to hand out the parsed
// document it would itself admit, so `delivered.Document().Generation += 7`
// changed what Admit consumed, with the attestation still covering the original
// bytes. core 67ee7220 made Document() re-derive from the attested payload per
// call. This host is what depends on that — it holds a derived document for the
// whole pass to attribute refusals and record desired state with — so a future
// pin that regressed to the shared pointer must come back red here rather than
// as a generation nobody signed being written to a row.
//
// The assertion is deliberately two-sided: it proves the mutation really landed
// on the caller's copy AND that the verdict ignored it, so it cannot pass by
// the mutation having silently failed to apply.
func TestSolutionHostBindingAdmissionReadsTheAttestedBytesNotTheCallersCopy(t *testing.T) {
	host, err := solutionhost.FixtureHost()
	if err != nil {
		t.Fatalf("fixture host: %v", err)
	}
	// The generation FixtureHost has already applied, so a sound reading is
	// DecisionCurrent and a mutated one would be DecisionApply.
	document := mustFixtureDocument(t, "valid")
	pairs := delivered(t, document)
	if len(pairs) != 1 {
		t.Fatalf("delivered %d carriers, want 1", len(pairs))
	}
	attested := pairs[0].Document.Generation
	pairs[0].Document.Generation = attested + 7

	admission := admitSolutionHostBindings(host, pairs)

	if reason, withheld := admission.Withheld[document.Binding]; withheld {
		t.Fatalf("the attested generation is the applied one and must be admitted, was withheld: %s", reason)
	}
	if len(admission.Accepted) != 1 {
		t.Fatalf("accepted %d documents, want 1", len(admission.Accepted))
	}
	if got := admission.Accepted[0].Document.Generation; got != attested+7 {
		t.Fatalf("the caller's copy reads generation %d, want %d: the mutation this test is about did not land, so the verdict below proves nothing",
			got, attested+7)
	}
	if got := admission.Accepted[0].Decision; got != solutionhost.DecisionCurrent {
		t.Fatalf("decision = %q, want %q: admission read generation %d from the caller's copy rather than %d from the attested bytes",
			got, solutionhost.DecisionCurrent, attested+7, attested)
	}
}

// A carrier whose bundle is absent, null or not an object is refused as
// unsigned, and the host must never read the document out of it.
//
// The field is the one a renderer with no signing step yet can fill: writing
// `null` costs nothing and produces a carrier that is structurally a carrier.
// Core looks inside no bundle — it holds no trust root — so "there is one" is
// the entire check available at this layer, and it is the difference between a
// signed document and a document.
func TestSolutionHostBindingRefusesACarrierWithNoBundle(t *testing.T) {
	document := mustFixtureDocument(t, "valid")
	canonical, err := document.CanonicalBytes()
	if err != nil {
		t.Fatalf("canonical bytes: %v", err)
	}
	for _, one := range []struct {
		name   string
		bundle string
	}{
		{"null", `null`},
		{"absent", ``},
		{"a string rather than an object", `"not-a-bundle"`},
		{"an array rather than an object", `[]`},
	} {
		t.Run(one.name, func(t *testing.T) {
			// Hand-written rather than built with MarshalSigned, which refuses
			// these itself: the point is what the host does with bytes that
			// reached its mount, not what its own writer would produce.
			field := ""
			if one.bundle != "" {
				field = `,"bundle":` + one.bundle
			}
			raw := []byte(`{"schema":"` + solutionhost.SchemaSignedV1 +
				`","document":` + string(canonical) + field + `}`)

			verified, problems := verifySolutionHostBindingDocuments(
				context.Background(), fixtureSigner(),
				[]SolutionHostBindingDocument{{Source: "unsigned.json", Data: raw}},
			)
			if len(verified) != 0 {
				t.Fatalf("admitted %d carriers with a %s bundle, want none", len(verified), one.name)
			}
			if len(problems) != 1 {
				t.Fatalf("recorded %d problems, want 1", len(problems))
			}
			if !errors.Is(problems[0].err, solutionhost.ErrUnsigned) {
				t.Fatalf("refusal %v is not ErrUnsigned, so the host refused it for some other reason", problems[0].err)
			}
		})
	}
}
