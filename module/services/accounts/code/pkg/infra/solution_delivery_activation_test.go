//go:build !pure

package infra_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"accounts/internal/deliverytest"
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
// The ceiling is the MODULE envelope, keyed by module principal and admitting
// unit IDs in delivery's convention (<presence binding>:<binding>:<operation>),
// so the documents here are built in that convention rather than taken from
// core's fixtures, whose principals and unit IDs no module envelope can name.
// The tuple still has to be core's: the approved build is the build the presence
// document's workload declares, both halves name the same envelope revision, and
// the authority is effective from a generation the presence has reached.

// TestDeliveredAuthorityActivatesAgainstDeliveredPresence drives the whole
// question through the host.
func TestDeliveredAuthorityActivatesAgainstDeliveredPresence(t *testing.T) {
	delivery := newProductionPathDelivery(t)

	solutionID := uniqueAlias("acv")
	namespace := "ns-" + solutionID
	presence := productionPathBinding(t, solutionID, namespace, 1)
	build := presence.Builds()[0]
	delivery.callerNamespace = namespace
	requireDelivered(t, delivery.post(t, business.SolutionDeliveryPresence, productionPathCarrier(t, presence)))
	authority := productionPathAuthority(t, solutionID, presence.Binding, "a", 1, build, false)
	delivery.callerNamespace = authorityWriterNamespace
	requireDelivered(t, delivery.post(t, business.SolutionDeliveryAuthority, productionPathAuthorityCarrier(t, authority)))

	// A second approved build the presence does not run, so the refusal below
	// is about the tuple rather than about the ceiling.
	other := solutionhost.FixtureEnvelope().ApprovedBuilds[1]
	require.NotEqual(t, build, other)
	activation := activationFor(t, delivery.service, productionPathCoordinate,
		moduleCeiling(solutionhost.FixtureEnvelopeRevision, []solutionhost.ImageDigest{build, other},
			solutionhost.EnvelopeGrant{Principal: productionPathPrincipal, Binding: authorityBindingFor(presence.Binding, namespace)}))
	require.NotNil(t, activation, "the reconciler exposes no activation seam, so the authority inbox is unread again")

	active, err := activation.Activate(testCtx, presence.Binding, build)
	require.NoError(t, err)
	require.Equal(t, authority.Authority, active.Authority)
	require.Equal(t, presence.Binding, active.Binding)
	require.Equal(t, build, active.Build)
	require.Equal(t, uint64(1), active.Generation, "the activation names the PRESENCE generation that activated it")
	require.Equal(t, uint64(solutionhost.FixtureEnvelopeRevision), active.EnvelopeRevision)
	require.Equal(t, productionPathDomain, active.Domain)
	require.Equal(t, productionPathCoordinate, active.Host.Coordinate,
		"an activation that did not say which host it was for could be read as an activation anywhere")

	// ANOTHER BUILD ACTIVATES NOTHING. This is the acceptance case for the whole
	// design: authority is held for the build that was reviewed, so shipping a
	// new build does not carry the old build's authority forward.
	_, err = activation.Activate(testCtx, presence.Binding, other)
	require.ErrorIs(t, err, solutionhost.ErrNotActivated)

	// PRESENCE ALONE GRANTS NOTHING. A binding delivered with no authority
	// document over it is not a refusal of a grant — nothing was granted — and
	// the two are distinct errors because an operator fixes them in different
	// places.
	onlyID := uniqueAlias("act")
	onlyNamespace := "ns-" + onlyID
	presenceOnly := productionPathBinding(t, onlyID, onlyNamespace, 1)
	delivery.callerNamespace = onlyNamespace
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

	// No envelope: what a host whose platform administrator has written no
	// ceiling holds.
	activation := fixtureActivation(t, delivery.service, nil)
	_, err := activation.Activate(testCtx, solutionhost.FixtureBindingID, fixturePresenceBuild(t))
	require.ErrorIs(t, err, business.ErrSolutionAuthorityCeilingUnavailable)
}

// fixtureActivation builds the activation seam for a host that answers for the
// FIXTURE coordinate and domain, which is what the shipped documents target.
func fixtureActivation(
	t *testing.T, service *business.Service, envelope *business.ModuleEnvelope,
) *business.SolutionAuthorityActivation {
	t.Helper()
	return activationFor(t, service, solutionhost.FixtureCoordinate, envelope)
}

// staticCeiling serves one ceiling by value, the way the store serves the
// current revision. A nil envelope is a host with no ceiling written.
type staticCeiling struct{ envelope *business.ModuleEnvelope }

func (c staticCeiling) CurrentModuleEnvelope(context.Context) (*business.ModuleEnvelope, error) {
	return c.envelope, nil
}

// productionPathPrincipal is the module principal the production-path documents
// grant to: a module's own name, which is what delivery writes.
const productionPathPrincipal = "operator"

// moduleCeiling is the module envelope that admits exactly the (principal,
// unit) pairs given, whole, at one revision, approving the builds given. Each
// unit's ID follows delivery's convention, and the principal's grant declares
// the operation audience, queue and namespace the unit names. These tests read
// activation and approved builds, which take no workload, so it allows none.
func moduleCeiling(
	revision uint64, builds []solutionhost.ImageDigest, grants ...solutionhost.EnvelopeGrant,
) *business.ModuleEnvelope {
	envelope := &business.ModuleEnvelope{
		Revision:       revision,
		ApprovedBuilds: builds,
		Principals:     map[string]business.ModuleEnvelopePrincipal{},
	}
	for _, grant := range grants {
		entry := envelope.Principals[grant.Principal]
		// The whole unit is the envelope's own, so a document whose unit
		// differs in any field is outside it.
		entry.Units = append(entry.Units, grant.Binding)
		parts := strings.Split(grant.Binding.ID, ":")
		if entry.Grant.OperationAudiences == nil {
			entry.Grant.OperationAudiences = map[string]business.ModuleOperationAudience{}
		}
		entry.Grant.OperationAudiences[parts[len(parts)-2]] = business.ModuleOperationAudience{Audience: grant.Binding.Audience}
		if grant.Binding.Queue != "" {
			entry.Grant.Queues = append(entry.Grant.Queues, grant.Binding.Queue)
		}
		if grant.Binding.Namespace != "" {
			entry.Grant.Namespaces = append(entry.Grant.Namespaces, grant.Binding.Namespace)
		}
		envelope.Principals[grant.Principal] = entry
	}
	return envelope
}

func activationFor(
	t *testing.T, service *business.Service, coordinate string, envelope *business.ModuleEnvelope,
) *business.SolutionAuthorityActivation {
	t.Helper()
	reconciler, err := business.NewSolutionHostBindingReconciler(service,
		business.SolutionHostBindingReconcilerConfig{
			Source:          business.NewDeliveredSolutionHostBindings(service),
			Verifier:        deliverytest.Verifier(t, map[string][]string{productionPathSigner: {solutionhost.FixtureDomain, productionPathDomain}}),
			Coordinate:      coordinate,
			Domains:         []string{solutionhost.FixtureDomain, productionPathDomain},
			DomainsBySigner: map[string][]string{productionPathSigner: {solutionhost.FixtureDomain, productionPathDomain}},
			Interval:        time.Minute,
			Envelopes:       staticCeiling{envelope: envelope},
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
		productionPathEnvelope(presence.Binding, namespace, build))

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
		// This subject owns no queues, audit namespaces or scope vocabulary.
		// Core requires explicit declarations, including on tombstones.
		Queues:        []string{},
		Namespaces:    []string{},
		ScopeCeilings: []solutionhost.ScopeCeiling{},
	}
	if !removed {
		document.ApprovedBuild = build
		document.EffectiveFrom = 1
		document.Principals = []solutionhost.PrincipalAuthority{{
			Principal: productionPathPrincipal,
			Bindings:  []solutionhost.AuthorityBinding{authorityBindingFor(binding, "ns-"+solutionID)},
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
	presenceBinding, namespace string, build solutionhost.ImageDigest,
) *business.ModuleEnvelope {
	return moduleCeiling(solutionhost.FixtureEnvelopeRevision, []solutionhost.ImageDigest{build},
		solutionhost.EnvelopeGrant{Principal: productionPathPrincipal, Binding: authorityBindingFor(presenceBinding, namespace)})
}

// authorityBindingFor is the one unit the production-path documents grant: the
// invoke operation of a `reconcile` binding over the presence binding, named as
// delivery names it.
func authorityBindingFor(presenceBinding, namespace string) solutionhost.AuthorityBinding {
	return solutionhost.AuthorityBinding{
		ID:        presenceBinding + ":reconcile:invoke",
		Revision:  1,
		Audience:  "https://test.acme.example/operations",
		Scope:     "reconcile:run",
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
	return deliverytest.Carrier(t, payload, productionPathSigner)
}
