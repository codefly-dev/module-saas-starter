//go:build !pure

package infra_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// THE APPROVED-BUILD VIEW, from the inbox the delivery endpoint writes.
//
// The defect these cover is an absence, like activation's: `MonotonicApprovedBuilds`
// held the authoritative view, enforced the ordering rule over it, and NOTHING
// EVER WROTE TO IT. Every principal was therefore unknown, `ApprovedBuild`
// refused every one, and so `BindExecution` — the whole execution-binding
// mechanism, built and unit-tested — could not answer a single question on a
// running host. A reviewer reading those unit tests and that guard concludes the
// check is enforced.
//
// Driven through the REAL delivery handler and read back out of real Postgres,
// so what is under test is the host's path from "a carrier arrived" to "this
// principal is approved for this build" — not a hand-assembled document handed
// to the reconciler, which would prove only that the reconciler reads a struct.
//
// THE INBOX IS HOST-WIDE, and that is not a test artifact — it is the fact that
// shaped the design. Every test in this package delivers into one inbox, and so
// does every solution in a running host. A pass therefore always reads
// documents it is not about, and `RunOnce` returns the reasons for every
// principal it refused, including other solutions'. So these tests assert on
// the VIEW, which is the outcome, and check separately that their own
// principals are not among the refusals. A test that required RunOnce to return
// no error at all would be asserting that no other solution ever delivers
// anything questionable.

// TestDeliveredAuthorityPopulatesTheApprovedBuildView is the regression test for
// the absence: before a pass the principal is UNKNOWN, after one it is approved
// for exactly what the delivered document approves.
func TestDeliveredAuthorityPopulatesTheApprovedBuildView(t *testing.T) {
	scenario := newApprovedBuildScenario(t, "abv")

	// Before any pass: unknown, not "bears none". A host that has reconciled
	// nothing refuses rather than minting unbound capabilities for everyone.
	_, _, err := scenario.reconciler.ApprovedBuild(testCtx, scenario.principal)
	require.ErrorIs(t, err, business.ErrUnknownExecutionPrincipal,
		"an unreconciled host must refuse, because an unbound capability for an unknown identity is the permissive answer")

	scenario.deliverAuthority(t, scenario.authority(t, "a", 3, false, scenario.principal))
	scenario.runPass(t)

	digest, incarnation, err := scenario.reconciler.ApprovedBuild(testCtx, scenario.principal)
	require.NoError(t, err)
	require.Equal(t, business.ApprovedDigest(scenario.build), digest,
		"the approved digest is the signed document's, which is the one input the caller has no influence over")
	require.Equal(t, uint64(3), incarnation,
		"the incarnation is the authority document's generation, which core guarantees is strictly monotonic per authority id")
}

// TestWithdrawnAuthorityMakesThePrincipalUnknownAgain is why the view is
// RECONSTRUCTED rather than written to.
//
// `MonotonicApprovedBuilds` has Approve and Declare and no third verb, so a
// withdrawal cannot be recorded into it — and the answer a withdrawal must
// produce is "unknown", not "bears no execution". Those are the two outcomes it
// would be easy to conflate and only one is safe: "bears none" mints a
// capability carrying no execution that every verifier accepts, which is right
// for a human session and wrong for a module whose authority was withdrawn.
func TestWithdrawnAuthorityMakesThePrincipalUnknownAgain(t *testing.T) {
	scenario := newApprovedBuildScenario(t, "abw")

	scenario.deliverAuthority(t, scenario.authority(t, "a", 1, false, scenario.principal))
	scenario.runPass(t)
	_, _, err := scenario.reconciler.ApprovedBuild(testCtx, scenario.principal)
	require.NoError(t, err, "the grant must take effect before its withdrawal can mean anything")

	scenario.deliverAuthority(t, scenario.authority(t, "a", 2, true, scenario.principal))
	scenario.runPass(t)

	_, _, err = scenario.reconciler.ApprovedBuild(testCtx, scenario.principal)
	require.ErrorIs(t, err, business.ErrUnknownExecutionPrincipal,
		"a withdrawn authority must leave the principal UNKNOWN; answering ErrNoApprovedBuild would let it mint an unbound capability")
}

// TestARewindAcrossPassesIsRefused is the test a fresh instance per pass cannot
// pass, and the reason the high-water marks outlive the view.
//
// Core's guidance is that a source which must rewind is reconstructing state and
// belongs behind a fresh instance. Applied naively that loses the guard: a fresh
// instance holds no previous record, so each pass accepts its own input as the
// first thing it ever saw, and generation 3 followed by generation 2 is accepted
// twice. The ordering rule is a property of the sequence of PASSES.
func TestARewindAcrossPassesIsRefused(t *testing.T) {
	scenario := newApprovedBuildScenario(t, "abr")

	scenario.deliverAuthority(t, scenario.authority(t, "a", 3, false, scenario.principal))
	scenario.runPass(t)
	_, incarnation, err := scenario.reconciler.ApprovedBuild(testCtx, scenario.principal)
	require.NoError(t, err)
	require.Equal(t, uint64(3), incarnation)

	// The inbox keeps the NEWEST generation per document, so a lower generation
	// has to arrive under a different authority id to be in the inbox at all —
	// which is also the shape that matters, since generations are monotonic per
	// authority id and two ids are not comparable. Withdraw the first so the
	// pass is not merely ambiguous, then deliver the lower one.
	scenario.deliverAuthority(t, scenario.authority(t, "a", 4, true, scenario.principal))
	scenario.deliverAuthority(t, scenario.authority(t, "b", 2, false, scenario.principal))

	reasons := scenario.reconciler.RunOnce(testCtx)
	require.ErrorIs(t, reasons, business.ErrApprovedBuildRewind,
		"a generation below the high-water mark must be refused, not accepted as a fresh start")
	require.Contains(t, reasons.Error(), scenario.principal,
		"the refusal must name the principal it is about, or an operator cannot act on it")

	// AND THE PRINCIPAL IS NOW UNKNOWN rather than still serving generation 3.
	// A rewind means delivery no longer claims the generation this host served,
	// so continuing to serve it would be answering with something nothing
	// claims. Omitted is refused, which is the safe direction.
	_, _, err = scenario.reconciler.ApprovedBuild(testCtx, scenario.principal)
	require.ErrorIs(t, err, business.ErrUnknownExecutionPrincipal)
}

// TestAPrincipalGrantedByTwoLiveAuthoritiesIsRefused covers the catch in using
// the generation as the incarnation.
//
// Generations are monotonic PER AUTHORITY ID, so two documents with different
// ids have independent sequences. If both name one principal their numbers are
// incomparable: the view would flap between them, and whenever the numbers
// happen to ascend the principal is silently resealed to the other document's
// build with no counter having gone backwards for the guard to catch.
func TestAPrincipalGrantedByTwoLiveAuthoritiesIsRefused(t *testing.T) {
	scenario := newApprovedBuildScenario(t, "aba")

	scenario.deliverAuthority(t, scenario.authority(t, "a", 1, false, scenario.principal))
	scenario.deliverAuthority(t, scenario.authority(t, "b", 1, false, scenario.principal))

	reasons := scenario.reconciler.RunOnce(testCtx)
	require.ErrorIs(t, reasons, business.ErrApprovedBuildAmbiguous,
		"two live authority documents granting to one principal have no single generation, so this is refused rather than resolved")
	require.Contains(t, reasons.Error(), scenario.principal)

	_, _, err := scenario.reconciler.ApprovedBuild(testCtx, scenario.principal)
	require.ErrorIs(t, err, business.ErrUnknownExecutionPrincipal)
}

// TestOneRefusedPrincipalDoesNotWithholdTheOthers is the correction a test
// found, and it is the reason the refusals are per principal.
//
// The first version of this reconciler installed NOTHING when any principal was
// refused, on the argument that installing the coherent part of an incoherent
// inbox is how a withheld document becomes a quietly narrower system. The inbox
// is host-wide, so that made one solution's bad delivery an outage for every
// module on the host — while being no safer for the principal actually in
// doubt, which is refused either way.
func TestOneRefusedPrincipalDoesNotWithholdTheOthers(t *testing.T) {
	// The second principal is named to the constructor so its grant is in the
	// ceiling the reconciler holds; see newApprovedBuildScenario.
	innocent := "principal:operator:" + uniqueAlias("abp") + ".innocent"
	scenario := newApprovedBuildScenario(t, "abp", innocent)

	// The scenario's own principal granted twice — ambiguous — and a second
	// principal granted once and correctly, in the same inbox.
	scenario.deliverAuthority(t, scenario.authority(t, "a", 1, false, scenario.principal))
	scenario.deliverAuthority(t, scenario.authority(t, "b", 1, false, scenario.principal, innocent))

	reasons := scenario.reconciler.RunOnce(testCtx)
	require.ErrorIs(t, reasons, business.ErrApprovedBuildAmbiguous)

	_, _, err := scenario.reconciler.ApprovedBuild(testCtx, scenario.principal)
	require.ErrorIs(t, err, business.ErrUnknownExecutionPrincipal,
		"the ambiguous principal is refused")

	digest, incarnation, err := scenario.reconciler.ApprovedBuild(testCtx, innocent)
	require.NoError(t, err,
		"a principal whose documents say exactly one thing must not be refused because an unrelated one is ambiguous")
	require.Equal(t, business.ApprovedDigest(scenario.build), digest)
	require.Equal(t, uint64(1), incarnation)
}

// TestAnAuthorityOutsideTheCeilingApprovesNothing is the check that an authentic
// signature is not an approval.
//
// A signed document says who wrote it. Whether what it claims is inside the
// ceiling a platform administrator wrote is a separate question, which core
// keeps as a separate call — and the clause that matters for this view is that
// the build must be one THE ENVELOPE approved. Without it a delivery writer
// approves any image by signing a document naming it, which would defeat the
// whole execution check this view feeds.
func TestAnAuthorityOutsideTheCeilingApprovesNothing(t *testing.T) {
	scenario := newApprovedBuildScenario(t, "abc")

	// A build the envelope does not approve. Core's second fixture build is
	// well-formed and not this scenario's, so the refusal is about the ceiling
	// rather than about a malformed digest.
	unapproved := solutionhost.FixtureEnvelope().ApprovedBuilds[1]
	require.NotEqual(t, scenario.build, unapproved)

	document := scenario.authority(t, "a", 1, false, scenario.principal)
	document.ApprovedBuild = unapproved
	require.NoError(t, document.Validate(),
		"the document must be VALID and merely outside the ceiling, or this tests document validation instead")
	scenario.deliverAuthority(t, document)

	reasons := scenario.reconciler.RunOnce(testCtx)
	require.Error(t, reasons)
	require.Contains(t, reasons.Error(), "outside this host's ceiling")

	_, _, err := scenario.reconciler.ApprovedBuild(testCtx, scenario.principal)
	require.ErrorIs(t, err, business.ErrUnknownExecutionPrincipal,
		"a document the ceiling does not cover must approve nothing, however well signed")
}

// TestAnUnreadableInboxLeavesThePreviousViewServing is the one failure that IS
// whole-pass, and the distinction is the point.
//
// A row this host can no longer verify leaves the approved state for EVERY
// principal unestablished, because which principals a row grants to is only
// knowable by reading it — and the one row whose disappearance grants something
// is a tombstone. So nothing is installed and the last good view keeps serving,
// which is a different answer from "no principal is approved".
func TestAnUnreadableInboxLeavesThePreviousViewServing(t *testing.T) {
	scenario := newApprovedBuildScenario(t, "abu")

	scenario.deliverAuthority(t, scenario.authority(t, "a", 2, false, scenario.principal))
	scenario.runPass(t)
	_, incarnation, err := scenario.reconciler.ApprovedBuild(testCtx, scenario.principal)
	require.NoError(t, err)
	require.Equal(t, uint64(2), incarnation)

	// A second reconciler over the SAME inbox whose verifier refuses every
	// carrier: the host's signer policy moved, which is exactly the case
	// re-verification on every pass exists for.
	refusing, err := business.NewApprovedBuildReconciler(
		activationForVerifier(t, scenario.delivery.service, productionPathCoordinate,
			scenario.envelope, refusingBundleVerifier{}))
	require.NoError(t, err)

	// It has its own empty view, so prove it refuses the read FIRST — then the
	// assertion below is about the pass rather than about a fresh instance.
	require.ErrorIs(t, refusing.RunOnce(testCtx), business.ErrApprovedBuildInboxUnreadable)

	// The ORIGINAL reconciler is untouched: a pass that could not read the
	// inbox installs nothing, so its view still serves what it last
	// established.
	_, incarnation, err = scenario.reconciler.ApprovedBuild(testCtx, scenario.principal)
	require.NoError(t, err)
	require.Equal(t, uint64(2), incarnation,
		"an unreadable inbox must not retract what a readable one established")
}

// approvedBuildScenario is one solution's delivery, its envelope, and the
// reconciler reading its inbox.
type approvedBuildScenario struct {
	delivery   *productionPathDelivery
	reconciler *business.ApprovedBuildReconciler
	envelope   solutionhost.Envelope
	binding    string
	build      solutionhost.ImageDigest
	solutionID string
	namespace  string
	principal  string
}

func newApprovedBuildScenario(t *testing.T, prefix string, extra ...string) *approvedBuildScenario {
	t.Helper()
	solutionID := uniqueAlias(prefix)
	namespace := "ns-" + solutionID
	build := fixturePresenceBuild(t)

	delivery := newProductionPathDelivery(t)
	presence := productionPathBinding(t, solutionID, namespace, 1)
	delivery.callerNamespace = namespace
	requireDelivered(t, delivery.post(t, business.SolutionDeliveryPresence,
		productionPathCarrier(t, presence)))

	scenario := &approvedBuildScenario{
		delivery:   delivery,
		binding:    presence.Binding,
		build:      build,
		solutionID: solutionID,
		namespace:  namespace,
		// Per scenario, so one test's inbox never makes another's principal
		// ambiguous. The shared harness grants to one fixed principal for every
		// solution, which is precisely the collision the ambiguity check
		// refuses — so these tests cannot reuse it.
		principal: "principal:operator:" + solutionID,
		envelope: solutionhost.Envelope{
			Revision:       solutionhost.FixtureEnvelopeRevision,
			ApprovedBuilds: []solutionhost.ImageDigest{build},
		},
	}
	// EVERY GRANT BEFORE THE ACTIVATION. The reconciler holds the envelope by
	// VALUE, so a grant appended afterwards is invisible to it and the document
	// naming that principal reads as outside the ceiling. The extra principals
	// are therefore constructor arguments rather than something a test adds
	// later — the first version of this test added one later and spent a run
	// proving that the ceiling check works.
	for _, principal := range append([]string{scenario.principal}, extra...) {
		scenario.grant(principal)
	}

	activation := activationFor(t, delivery.service, productionPathCoordinate, scenario.envelope)
	require.NotNil(t, activation)
	reconciler, err := business.NewApprovedBuildReconciler(activation)
	require.NoError(t, err)
	scenario.reconciler = reconciler
	return scenario
}

// grant adds a principal and its own binding to the scenario's ceiling.
//
// Its OWN binding, because core refuses a document in which two principals hold
// one binding ID — "an ID identifies one unit of authority" — so a second
// principal needs a second unit, both in the document and in the envelope that
// has to hold it exactly.
//
// It is unexported and called only from the constructor, because the envelope
// reaches the reconciler by VALUE: a grant appended after the activation is
// built is invisible to it, and the document naming that principal then reads
// as outside the ceiling — a confusing way to fail, and one this test hit
// before the extra principals became constructor arguments.
func (s *approvedBuildScenario) grant(principal string) {
	s.envelope.Grants = append(s.envelope.Grants, solutionhost.EnvelopeGrant{
		Principal: principal,
		Binding:   s.bindingFor(principal),
	})
}

func (s *approvedBuildScenario) bindingFor(principal string) solutionhost.AuthorityBinding {
	binding := authorityBindingFor(s.solutionID, s.namespace)
	// One unit of authority per principal, named after it so the two halves
	// cannot drift apart.
	binding.ID = "binding:" + principal + ":reconcile"
	return binding
}

// runPass runs a reconcile pass and requires that this scenario's own
// principals are not among whatever it refused.
//
// It deliberately does NOT require the pass to be error-free. The inbox is
// host-wide: a pass reads every other test's documents, and other solutions'
// principals are legitimately refused in it. Requiring no error would make
// every test here depend on every other test's deliveries being beyond
// question, which is the contamination that shaped the per-principal design in
// the first place.
func (s *approvedBuildScenario) runPass(t *testing.T) {
	t.Helper()
	if err := s.reconciler.RunOnce(testCtx); err != nil {
		require.NotContains(t, err.Error(), s.principal,
			"the pass refused this scenario's own principal, which no assertion here expects")
		t.Logf("pass refused principals belonging to other deliveries in the shared inbox: %v", err)
	}
}

func (s *approvedBuildScenario) deliverAuthority(t *testing.T, document *solutionhost.AuthorityDocument) {
	t.Helper()
	s.delivery.callerNamespace = authorityWriterNamespace
	requireDelivered(t, s.delivery.post(t, business.SolutionDeliveryAuthority,
		productionPathAuthorityCarrier(t, document)))
}

// authority builds one authority document over this scenario's binding, naming
// the principals given.
//
// It takes the principals rather than using the shared harness's single fixed
// one, because every assertion here is about which principal a document grants
// to — a builder that always named the same one could not express "two
// documents, one principal" at all.
func (s *approvedBuildScenario) authority(
	t *testing.T, suffix string, generation uint64, removed bool, principals ...string,
) *solutionhost.AuthorityDocument {
	t.Helper()
	document := &solutionhost.AuthorityDocument{
		Schema:           solutionhost.SchemaAuthorityV1,
		Authority:        "acme.test." + s.solutionID + "." + suffix,
		Generation:       generation,
		PresenceBinding:  s.binding,
		Host:             solutionhost.HostTarget{Coordinate: productionPathCoordinate, Component: "saas-host"},
		OwnershipDomain:  productionPathDomain,
		EnvelopeRevision: solutionhost.FixtureEnvelopeRevision,
	}
	if removed {
		// A withdrawn generation names no principal, approves no build and is
		// effective from no generation.
		document.Removed = true
	} else {
		document.ApprovedBuild = s.build
		document.EffectiveFrom = 1
		for _, principal := range principals {
			document.Principals = append(document.Principals, solutionhost.PrincipalAuthority{
				Principal: principal,
				Bindings:  []solutionhost.AuthorityBinding{s.bindingFor(principal)},
			})
		}
	}
	require.NoError(t, document.Validate(), "the fixture must be a valid authority document")
	return document
}

// refusingBundleVerifier attests nothing, which is what a host whose signer
// policy no longer admits a delivery writer looks like to a row already stored.
//
// It refuses at the BUNDLE rather than by returning an unexpected signer
// identity, so the refusal happens in core's verification and not in this
// host's domain policy — the two produce different errors, and the one under
// test is "this row can no longer be verified at all".
type refusingBundleVerifier struct{}

func (refusingBundleVerifier) VerifyBundle(
	context.Context, []byte, json.RawMessage,
) (string, error) {
	return "", errors.New("this signer is no longer admitted")
}

// activationForVerifier is activationFor with the attestation check as a
// parameter, for the one test that needs a reader over the same inbox that can
// no longer verify it.
func activationForVerifier(
	t *testing.T, service *business.Service, coordinate string,
	envelope solutionhost.Envelope, verifier solutionhost.BundleVerifier,
) *business.SolutionAuthorityActivation {
	t.Helper()
	reconciler, err := business.NewSolutionHostBindingReconciler(service,
		business.SolutionHostBindingReconcilerConfig{
			Source:          business.NewDeliveredSolutionHostBindings(service),
			Verifier:        verifier,
			Coordinate:      coordinate,
			Domains:         []string{solutionhost.FixtureDomain, productionPathDomain},
			DomainsBySigner: map[string][]string{productionPathSigner: {solutionhost.FixtureDomain, productionPathDomain}},
			Interval:        time.Minute,
			Envelope:        envelope,
		})
	require.NoError(t, err)
	return reconciler.AuthorityActivation()
}
