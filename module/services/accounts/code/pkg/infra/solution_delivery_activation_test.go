//go:build !pure

package infra_test

import (
	"encoding/json"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// ACTIVATION, from the inbox the delivery endpoint writes.
//
// The defect this covers is an absence rather than a wrong answer: the endpoint
// accepted authority documents, persisted them, and NOTHING EVER READ ONE. There
// was no call site for core's Activate anywhere in this host, so a reviewed,
// signed, envelope-bound grant could be delivered and the host would behave
// exactly as if it had not been.
//
// Both halves are POSTed through the real handler and read back out of real
// Postgres, so what is under test is the host's own path from "a carrier
// arrived" to "this tuple is active" — not a hand-assembled pair of documents
// handed straight to core, which proves core.
//
// core's own fixtures are the documents, and here that is load-bearing rather
// than tidy: the authority fixture, the presence fixture and FixtureEnvelope are
// a matched tuple core ships BECAUSE getting one is fiddly — the approved build
// must be the build the presence document's workload declares, both halves must
// name the same envelope revision, and the authority must be effective from a
// generation the presence document has reached. A tuple this repository invented
// would agree with this repository's reading of those rules by construction.

// TestDeliveredAuthorityActivatesAgainstDeliveredPresence drives the whole
// question through the host.
func TestDeliveredAuthorityActivatesAgainstDeliveredPresence(t *testing.T) {
	delivery := newProductionPathDelivery(t)

	delivery.callerNamespace = fixturePresenceNamespace
	requireDelivered(t, delivery.post(t, business.SolutionDeliveryPresence, signedFixture(t, "presence")))
	delivery.callerNamespace = authorityWriterNamespace
	requireDelivered(t, delivery.post(t, business.SolutionDeliveryAuthority, signedFixture(t, "authority")))

	activation := fixtureActivation(t, delivery.service, solutionhost.FixtureEnvelope())
	require.NotNil(t, activation, "the reconciler exposes no activation seam, so the authority inbox is unread again")

	build := fixturePresenceBuild(t)
	active, err := activation.Activate(testCtx, solutionhost.FixtureBindingID, build)
	require.NoError(t, err)
	require.Equal(t, fixtureAuthorityID, active.Authority)
	require.Equal(t, solutionhost.FixtureBindingID, active.Binding)
	require.Equal(t, build, active.Build)
	require.Equal(t, uint64(4), active.Generation, "the activation names the PRESENCE generation that activated it")
	require.Equal(t, uint64(solutionhost.FixtureEnvelopeRevision), active.EnvelopeRevision)
	require.Equal(t, solutionhost.FixtureDomain, active.Domain)
	require.Equal(t, solutionhost.FixtureCoordinate, active.Host.Coordinate,
		"an activation that did not say which host it was for could be read as an activation anywhere")

	// ANOTHER BUILD ACTIVATES NOTHING. This is the acceptance case for the whole
	// design: authority is held for the build that was reviewed, so shipping a
	// new build does not carry the old build's authority forward. The second
	// approved build in FixtureEnvelope is approved and is not the one the
	// presence document runs, so the refusal is about the tuple rather than
	// about the ceiling.
	other := solutionhost.FixtureEnvelope().ApprovedBuilds[1]
	require.NotEqual(t, build, other)
	_, err = activation.Activate(testCtx, solutionhost.FixtureBindingID, other)
	require.ErrorIs(t, err, solutionhost.ErrNotActivated)

	// PRESENCE ALONE GRANTS NOTHING. A binding delivered with no authority
	// document over it is not a refusal of a grant — nothing was granted — and
	// the two are distinct errors because an operator fixes them in different
	// places.
	solutionID := uniqueAlias("act")
	namespace := "ns-" + solutionID
	presenceOnly := productionPathBinding(t, solutionID, namespace, 1)
	delivery.callerNamespace = namespace
	requireDelivered(t, delivery.post(t, business.SolutionDeliveryPresence,
		productionPathCarrier(t, presenceOnly)))
	_, err = activation.Activate(testCtx, presenceOnly.Binding, build)
	require.ErrorIs(t, err, business.ErrSolutionAuthorityNotDelivered)
}

// NO CEILING MEANS NO ANSWER, not a permissive one.
//
// A host with no envelope delivered has decided nothing about authority, which
// is a different fact from deciding that a grant is absent — and the difference
// is the whole reason the error is its own sentinel. A zero envelope that fell
// through to core would be refused there too, by the "no envelope revision was
// named" guard, but with a message about a tuple rather than about this host's
// configuration.
func TestWithoutAnEnvelopeTheHostActivatesNothing(t *testing.T) {
	delivery := newProductionPathDelivery(t)
	delivery.callerNamespace = fixturePresenceNamespace
	requireDelivered(t, delivery.post(t, business.SolutionDeliveryPresence, signedFixture(t, "presence")))
	delivery.callerNamespace = authorityWriterNamespace
	requireDelivered(t, delivery.post(t, business.SolutionDeliveryAuthority, signedFixture(t, "authority")))

	// The zero envelope: what a host that was delivered no ceiling holds.
	activation := fixtureActivation(t, delivery.service, solutionhost.Envelope{})
	_, err := activation.Activate(testCtx, solutionhost.FixtureBindingID, fixturePresenceBuild(t))
	require.ErrorIs(t, err, business.ErrSolutionAuthorityCeilingUnavailable)
}

// fixtureActivation builds the activation seam for a host that answers for the
// FIXTURE coordinate and domain, which is what the shipped documents target.
func fixtureActivation(
	t *testing.T, service *business.Service, envelope solutionhost.Envelope,
) *business.SolutionAuthorityActivation {
	t.Helper()
	return activationFor(t, service, solutionhost.FixtureCoordinate, envelope)
}

func activationFor(
	t *testing.T, service *business.Service, coordinate string, envelope solutionhost.Envelope,
) *business.SolutionAuthorityActivation {
	t.Helper()
	reconciler, err := business.NewSolutionHostBindingReconciler(service,
		business.SolutionHostBindingReconcilerConfig{
			Source:          business.NewDeliveredSolutionHostBindings(service),
			Verifier:        productionPathVerifier{signer: productionPathSigner},
			Coordinate:      coordinate,
			Domains:         []string{solutionhost.FixtureDomain, productionPathDomain},
			DomainsBySigner: map[string][]string{productionPathSigner: {solutionhost.FixtureDomain, productionPathDomain}},
			Interval:        time.Minute,
			Envelope:        envelope,
		})
	require.NoError(t, err)
	return reconciler.AuthorityActivation()
}

// fixturePresenceBuild is the image digest core's valid presence fixture's
// workload declares — read out of the fixture rather than written down, so a
// fixture whose build moves fails here instead of silently testing a build no
// document names.
func fixturePresenceBuild(t *testing.T) solutionhost.ImageDigest {
	t.Helper()
	document, err := solutionhost.Parse(presenceFixtureDocument(t))
	require.NoError(t, err)
	builds := document.Builds()
	require.Len(t, builds, 1)
	return builds[0]
}

func presenceFixtureDocument(t *testing.T) []byte {
	t.Helper()
	data, err := solutionhost.FixtureDocument(solutionhost.DocumentTypePresence, "valid")
	require.NoError(t, err)
	return data
}

// A WITHDRAWN AUTHORITY IS NOT REINSTATED BY RENAMING IT.
//
// This is the one part of core's generation fold the inbox cannot perform by
// itself, so the host performs it explicitly — and the reason it has to is
// specific. core keys the authority fold on the BINDING rather than on the
// authority id, precisely because an authority re-signed under a NEW id has no
// applied record of its own and would therefore read as a first generation. This
// host holds no applied authority record at all, so that fold is not available
// to it: what stands in is reading EVERY delivered authority document over the
// binding and refusing when a tombstone is among them. Nothing deletes from the
// inbox, so a withdrawal stays visible forever, which is what makes the
// substitute equivalent rather than merely similar.
//
// Core's own authority tombstone fixture is deliberately NOT used here. It
// carries the same authority id as the valid fixture at a higher generation, and
// this package's inbox is a shared, append-only table with no DELETE grant — so
// delivering it would supersede the valid document for every later run of
// TestDeliveredAuthorityActivatesAgainstDeliveredPresence. The documents here
// are this file's own, over a binding id unique to the run.
func TestAWithdrawnAuthorityIsNotReinstatedUnderANewID(t *testing.T) {
	solutionID := uniqueAlias("wd")
	namespace := "ns-" + solutionID
	presence := productionPathBinding(t, solutionID, namespace, 1)
	build := presence.Builds()[0]

	delivery := newProductionPathDelivery(t)
	delivery.callerNamespace = namespace
	requireDelivered(t, delivery.post(t, business.SolutionDeliveryPresence,
		productionPathCarrier(t, presence)))

	// First the grant, then its withdrawal, then the SAME grant under a new
	// authority id — which is the move the check exists to refuse.
	granted := productionPathAuthority(t, solutionID, presence.Binding, "a", 1, build, false)
	withdrawn := productionPathAuthority(t, solutionID, presence.Binding, "a", 2, build, true)
	renamed := productionPathAuthority(t, solutionID, presence.Binding, "b", 1, build, false)

	activation := activationFor(t, delivery.service, productionPathCoordinate,
		productionPathEnvelope(solutionID, namespace, build))

	delivery.callerNamespace = authorityWriterNamespace
	requireDelivered(t, delivery.post(t, business.SolutionDeliveryAuthority,
		productionPathAuthorityCarrier(t, granted)))
	active, err := activation.Activate(testCtx, presence.Binding, build)
	require.NoError(t, err, "the grant must activate before its withdrawal can mean anything")
	require.Equal(t, granted.Authority, active.Authority)

	requireDelivered(t, delivery.post(t, business.SolutionDeliveryAuthority,
		productionPathAuthorityCarrier(t, withdrawn)))
	_, err = activation.Activate(testCtx, presence.Binding, build)
	require.ErrorIs(t, err, business.ErrSolutionAuthorityWithdrawn)

	requireDelivered(t, delivery.post(t, business.SolutionDeliveryAuthority,
		productionPathAuthorityCarrier(t, renamed)))
	_, err = activation.Activate(testCtx, presence.Binding, build)
	require.ErrorIs(t, err, business.ErrSolutionAuthorityWithdrawn,
		"a withdrawal is a statement about the BINDING, so a new authority id over it reinstates nothing")
}

// productionPathAuthority is one authority document over one binding, under the
// authority id `acme.test.<solution>.<suffix>`.
func productionPathAuthority(
	t *testing.T, solutionID, binding, suffix string, generation uint64,
	build solutionhost.ImageDigest, removed bool,
) *solutionhost.AuthorityDocument {
	t.Helper()
	document := &solutionhost.AuthorityDocument{
		Schema:           solutionhost.SchemaAuthorityV1,
		Authority:        "acme.test." + solutionID + "." + suffix,
		Generation:       generation,
		PresenceBinding:  binding,
		Host:             solutionhost.HostTarget{Coordinate: productionPathCoordinate, Component: "saas-host"},
		OwnershipDomain:  productionPathDomain,
		EnvelopeRevision: solutionhost.FixtureEnvelopeRevision,
		// Core requires the subject module's three declarations on every
		// generation, [] when it has none: a nil list reads as one a renderer dropped.
		Queues:        []string{},
		Namespaces:    []string{},
		ScopeCeilings: []solutionhost.ScopeCeiling{},
	}
	if !removed {
		document.ApprovedBuild = build
		document.EffectiveFrom = 1
		document.Principals = []solutionhost.PrincipalAuthority{{
			Principal: "principal:operator",
			Bindings:  []solutionhost.AuthorityBinding{authorityBindingFor(solutionID, "ns-"+solutionID)},
		}}
	} else {
		// A withdrawn generation names no principal, approves no build and is
		// effective from no generation. Carrying any of those would be a
		// withdrawal that still says what it grants.
		document.Removed = true
	}
	require.NoError(t, document.Validate(), "the fixture must be a valid authority document")
	return document
}

// productionPathEnvelope is the ceiling the documents above sit inside. It is
// assembled by the TEST rather than read out of a document, which is the rule:
// an envelope a document carried would be a document declaring its own ceiling.
func productionPathEnvelope(
	solutionID, namespace string, build solutionhost.ImageDigest,
) solutionhost.Envelope {
	return solutionhost.Envelope{
		Revision: solutionhost.FixtureEnvelopeRevision,
		Grants: []solutionhost.EnvelopeGrant{{
			Principal: "principal:operator",
			Binding:   authorityBindingFor(solutionID, namespace),
		}},
		ApprovedBuilds: []solutionhost.ImageDigest{build},
	}
}

func authorityBindingFor(solutionID, namespace string) solutionhost.AuthorityBinding {
	return solutionhost.AuthorityBinding{
		ID:        "binding:" + solutionID + ":reconcile",
		Revision:  1,
		Audience:  "https://test.acme.example/operations",
		Scope:     "reconcile",
		Queue:     "reconcile.default",
		Namespace: namespace,
	}
}

// productionPathAuthorityCarrier wraps an authority document in the carrier
// delivery POSTs. SignedPayloadFor, not Marshal: the signing input is the
// canonical encoding, and AuthorityFromVerified refuses a payload that is not
// the canonical encoding of the document it decodes to.
func productionPathAuthorityCarrier(t *testing.T, document *solutionhost.AuthorityDocument) []byte {
	t.Helper()
	payload, err := solutionhost.SignedPayloadFor(document)
	require.NoError(t, err)
	carrier, err := solutionhost.MarshalSigned(&solutionhost.Signed{
		Schema:   solutionhost.SchemaSignedV1,
		Document: payload,
		Bundle:   json.RawMessage(solutionhost.FixtureBundle),
	})
	require.NoError(t, err)
	return carrier
}
