//go:build !pure

package infra_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"accounts/pkg/adapters"
	"accounts/pkg/business"
	"accounts/pkg/infra"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// The delivery endpoint, END TO END: the real HTTP handler, the real
// TokenReview carrier check, the real Postgres inbox, and the real reconciler
// reading that inbox.
//
// WHY THIS FILE EXISTS RATHER THAN MORE HANDLER TESTS. The handler tests beside
// `solution_delivery_http.go` wire the service's whole delivery half out of
// fakes: a fake verifier, a fake inbox, a fake carrier authorizer. That is the
// right shape for the response-code table and the wrong shape for everything
// else, because a fake standing in for the thing under test proves the caller
// and never the thing. Three defects survived that suite:
//
//   - both paths verified with the PRESENCE reader, so an authority document
//     could not be delivered at all and a presence document POSTed to
//     /authority was stored as kind=authority;
//   - a tombstone declares no workload and therefore no namespace, so the
//     carrier check refused it and REMOVAL THROUGH THE API WAS IMPOSSIBLE;
//   - nothing read the inbox, because the reconciler's source was the mount.
//
// None of the three is visible when the inbox and the carrier check are fakes.
// So these drive the real ones.
//
// ONE FAKE REMAINS, deliberately and with a reason that is not convenience: the
// BundleVerifier. core's signed fixtures carry `FixtureBundle`, a documented
// PLACEHOLDER — core verifies no attestation and ships none, because a real
// bundle needs a trust root, a transparency-log entry and a certificate that
// expires. Real keyless verification is exercised against an in-process
// Sigstore in solution_host_keyless_verifier_test.go, which is where the
// cryptography belongs. What is under test here is the host's ordering,
// dispatch, persistence and reconcile, all of which are real.

// The signer identity this host attests delivery as for the fixtures. It is
// core's own, because the shipped documents are written against it.
const productionPathSigner = solutionhost.FixtureDeliveredBy

// deliveryWriter is the one service account that delivers, for either kind.
const deliveryWriterAccount = "delivery"

// authorityWriterNamespace is the authority half's fixed writer namespace, as
// the carrier check requires it.
const authorityWriterNamespace = "platform-authority"

// productionPathDelivery is the whole receiving side, assembled from the real
// pieces.
type productionPathDelivery struct {
	service *business.Service
	handler http.Handler

	// callerNamespace is the namespace the fake API SERVER authenticates the
	// caller in. A fake api server rather than a fake carrier check: the
	// namespace comparison, the service-account comparison and the audience on
	// the review are all real, and a fake check would let every one of them go
	// untested while looking identical.
	callerNamespace string
}

func newProductionPathDelivery(t *testing.T) *productionPathDelivery {
	t.Helper()
	delivery := &productionPathDelivery{callerNamespace: authorityWriterNamespace}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/authentication.k8s.io/v1/tokenreviews" {
			http.NotFound(w, r)
			return
		}
		response := map[string]any{"status": map[string]any{
			"authenticated": true,
			"audiences":     []string{"accounts"},
			"user": map[string]any{
				"username": "system:serviceaccount:" + delivery.callerNamespace + ":" + deliveryWriterAccount,
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	t.Cleanup(server.Close)

	service, err := business.NewService(testStore)
	require.NoError(t, err)
	service.SetSolutionDelivery(
		productionPathVerifier{signer: productionPathSigner},
		testStore,
		infra.NewSolutionDeliveryCarrierCheck(infra.NewKubernetesClientForTest(t, server)),
		map[string][]string{
			productionPathSigner: {solutionhost.FixtureDomain, productionPathDomain},
		},
	)
	delivery.service = service
	delivery.handler = adapters.NewSolutionDeliveryHTTPHandler(service)
	return delivery
}

// post delivers one carrier to one kind's path, as the Job does.
func (d *productionPathDelivery) post(
	t *testing.T, kind business.SolutionDeliveryKind, body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost,
		adapters.SolutionDeliveryPrefix+string(kind), strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer projected-delivery-token")
	recorder := httptest.NewRecorder()
	d.handler.ServeHTTP(recorder, request)
	return recorder
}

// inbox reads the newest delivered generation per document for one kind,
// through the store method the reconciler itself reads with.
func inbox(t *testing.T, kind business.SolutionDeliveryKind) map[string]*business.SolutionDeliveryRecord {
	t.Helper()
	var records []*business.SolutionDeliveryRecord
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		records, err = testStore.ListNewestDeliveredDocuments(ctx, kind)
		return err
	}))
	byID := make(map[string]*business.SolutionDeliveryRecord, len(records))
	for _, record := range records {
		byID[record.DocumentID] = record
	}
	return byID
}

type productionPathVerifier struct{ signer string }

func (v productionPathVerifier) VerifyBundle(
	context.Context, []byte, json.RawMessage,
) (string, error) {
	return v.signer, nil
}

// ---------------------------------------------------------------------------
// The two kinds are two readers
// ---------------------------------------------------------------------------

// CORE'S OWN SIGNED FIXTURES, one per kind, through the real handler: each is
// accepted on its own path and refused on the other's.
//
// The fixtures are the point. A document this repository writes for itself
// agrees with this repository's reading of the schema by construction; core's
// are the bytes the renderer produces and the three repositories share. And the
// cross-kind half is what the inverted dispatch could not do: before it, the
// genuine authority fixture was REFUSED at /authority with "schema not
// supported", while the presence fixture POSTed to /authority was accepted and
// filed as kind=authority.
//
// The refusal is asserted as 400 rather than merely "an error". The bytes are
// wrong, not the signer: a 403 would send an operator to look at a signing
// identity that signed the document perfectly well and was simply sent to the
// wrong path.
func TestSignedFixturesAreAcceptedOnTheirOwnKindAndRefusedOnTheOther(t *testing.T) {
	presence := signedFixture(t, "presence")
	authority := signedFixture(t, "authority")

	delivery := newProductionPathDelivery(t)

	// The presence fixture's workloads declare namespace alpha-region-a-01, and
	// the carrier must authenticate there.
	delivery.callerNamespace = fixturePresenceNamespace
	accepted := delivery.post(t, business.SolutionDeliveryPresence, presence)
	requireDelivered(t, accepted)
	presenceRow := inbox(t, business.SolutionDeliveryPresence)[solutionhost.FixtureBindingID]
	require.NotNil(t, presenceRow, "the presence fixture is not in the inbox under its binding id")
	require.Equal(t, uint64(4), presenceRow.Generation)
	require.Equal(t, solutionhost.FixtureDomain, presenceRow.OwnershipDomain)
	require.Equal(t, productionPathSigner, presenceRow.SignerIdentity)

	delivery.callerNamespace = authorityWriterNamespace
	accepted = delivery.post(t, business.SolutionDeliveryAuthority, authority)
	requireDelivered(t, accepted)
	authorityRow := inbox(t, business.SolutionDeliveryAuthority)[fixtureAuthorityID]
	require.NotNil(t, authorityRow,
		"the authority fixture is not in the inbox: an authority document could not be delivered at all")
	require.Equal(t, uint64(2), authorityRow.Generation)
	require.Equal(t, fixtureAuthorityID, authorityRow.DocumentID,
		"an authority row is keyed by its OWN id, never by the presence binding it is granted over")

	// Now each on the other's path.
	refused := delivery.post(t, business.SolutionDeliveryAuthority, presence)
	require.Equal(t, http.StatusBadRequest, refused.Code,
		"a presence document POSTed to /authority is wrong BYTES for that path, not a wrong signer")
	require.NotContains(t, inbox(t, business.SolutionDeliveryAuthority), solutionhost.FixtureBindingID,
		"a presence document was filed as kind=authority")

	delivery.callerNamespace = fixturePresenceNamespace
	refused = delivery.post(t, business.SolutionDeliveryPresence, authority)
	require.Equal(t, http.StatusBadRequest, refused.Code,
		"an authority document POSTed to /presence is wrong BYTES for that path")
	require.NotContains(t, inbox(t, business.SolutionDeliveryPresence), fixtureAuthorityID,
		"an authority document was filed as kind=presence")
}

// fixturePresenceNamespace is the namespace core's valid presence fixture
// declares through its workload's SPIFFE ID, which is what its carrier is
// authorised against.
const fixturePresenceNamespace = "alpha-region-a-01"

// fixtureAuthorityID is the authority id core's valid authority fixture
// declares. It is deliberately NOT FixtureBindingID: an authority document is
// identified by its own id and granted over a binding, and conflating the two
// is what let a withdrawal be reinstated by renaming.
const fixtureAuthorityID = "alpha-region-a-01-authority"

// signedFixture returns one of core's shipped signed carriers by name, through
// FixturesOf so a fixture that is renamed or dropped fails here rather than
// silently stopping being exercised.
func signedFixture(t *testing.T, name string) []byte {
	t.Helper()
	for _, fixture := range solutionhost.FixturesOf(solutionhost.DocumentTypeSigned) {
		if fixture.Name == name {
			require.Equal(t, solutionhost.OutcomeAccepted, fixture.Outcome,
				"this test drives the ACCEPTED fixtures; %q is now expected to be rejected", name)
			return fixture.Document
		}
	}
	t.Fatalf("core ships no signed fixture named %q", name)
	return nil
}

// requireDelivered accepts 202 and 200 and nothing else.
//
// Both are success and the difference is whether anything changed. The inbox is
// append-only with no DELETE grant, so a fixture whose binding id and
// generation are fixed is 202 the first time this package's database sees it and
// 200 on every run after — and a test that demanded 202 would pass once and then
// fail forever for a reason that has nothing to do with the behaviour.
func requireDelivered(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusAccepted && recorder.Code != http.StatusOK {
		t.Fatalf("delivery was refused with %d: %s", recorder.Code, recorder.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Removal through the API, end to end
// ---------------------------------------------------------------------------

// productionPathDomain is the ownership domain this file's own documents are
// delivered under, and productionPathCoordinate the host they target.
const (
	productionPathDomain     = "acme"
	productionPathCoordinate = "acme/test/eu-west-1"
)

// A TOMBSTONE IS ACCEPTED AND THE BINDING IS REMOVED, through the real endpoint
// and the real inbox.
//
// This is the defect that made the delivery API unable to express the one thing
// the whole design rests on. Removal is a GENERATION — nothing deletes from the
// inbox and nothing deletes a binding — and a tombstone declares absence, so it
// carries no workload and therefore no workload namespace. The carrier check
// authorised a presence carrier against the namespaces the document's own
// workloads declare and refused the empty set by name, so every tombstone was
// 403 and a binding delivered through this endpoint could never be withdrawn
// through it.
//
// The fix is not to relax the check. It is to authorise the carrier against the
// namespace this host LAST APPLIED for that binding: its own recorded state,
// from a generation it already verified and admitted, rather than anything the
// arriving document asserts.
//
// The whole lifecycle is driven here because each half alone is satisfiable
// without the other: a tombstone accepted but never reconciled leaves the
// solution serving, and a reconcile that withdraws without the POST having been
// accepted is the state the fake-sourced tests were already in.
func TestTombstoneIsAcceptedAndWithdrawsTheRegistrationEndToEnd(t *testing.T) {
	solutionID := uniqueAlias("rm")
	namespace := "ns-" + solutionID
	present := productionPathBinding(t, solutionID, namespace, 1)

	delivery := newProductionPathDelivery(t)
	delivery.callerNamespace = namespace
	requireDelivered(t, delivery.post(t, business.SolutionDeliveryPresence,
		productionPathCarrier(t, present)))

	reconciler := newProductionPathReconciler(t, delivery.service)
	require.NoError(t, reconciler.RunOnce(testCtx))

	applied := productionPathBindingRecord(t, delivery.service, present.Binding)
	require.NotNil(t, applied.Applied, "the delivered generation was never applied from the inbox")
	require.Equal(t, uint64(1), applied.Applied.Generation)
	require.Equal(t, solutionID, applied.Applied.SolutionID)
	require.False(t, applied.Applied.Removed)

	// THE POST THAT WAS IMPOSSIBLE. The tombstone declares no workload, so its
	// carrier is authorised against the namespace generation 1 applied.
	tombstone := productionPathTombstone(t, present, 2)
	receipt := delivery.post(t, business.SolutionDeliveryPresence, productionPathCarrier(t, tombstone))
	if receipt.Code == http.StatusForbidden {
		t.Fatalf("the tombstone's carrier was refused, so removal through the API is impossible: %s",
			receipt.Body.String())
	}
	requireDelivered(t, receipt)

	// The inbox's desired set is the NEWEST generation per document, so the
	// tombstone supersedes generation 1 without anything being deleted.
	row := inbox(t, business.SolutionDeliveryPresence)[present.Binding]
	require.NotNil(t, row)
	require.Equal(t, uint64(2), row.Generation)

	require.NoError(t, reconciler.RunOnce(testCtx))
	withdrawn := productionPathBindingRecord(t, delivery.service, present.Binding)
	require.NotNil(t, withdrawn.Applied)
	require.Equal(t, uint64(2), withdrawn.Applied.Generation)
	require.True(t, withdrawn.Applied.Removed, "the tombstone generation did not apply")
	require.Equal(t, solutionID, withdrawn.Applied.SolutionID,
		"a tombstone keeps the key it withdrew, so the removal stays attributable")

	// And the registry record the solution was served under is tombstoned, which
	// is what actually takes it out of service.
	registrations, _, err := delivery.service.ListSolutionRegistrations(testCtx, true)
	require.NoError(t, err)
	for _, registration := range registrations {
		if registration.SolutionID == solutionID {
			require.NotNil(t, registration.TombstonedAt,
				"the registration is still live after the binding was withdrawn")
		}
	}
}

// newProductionPathReconciler builds the reconciler over the DURABLE INBOX,
// which is the only source there is.
func newProductionPathReconciler(
	t *testing.T, service *business.Service,
) *business.SolutionHostBindingReconciler {
	t.Helper()
	reconciler, err := business.NewSolutionHostBindingReconciler(service,
		business.SolutionHostBindingReconcilerConfig{
			Source:          business.NewDeliveredSolutionHostBindings(service),
			Verifier:        productionPathVerifier{signer: productionPathSigner},
			Coordinate:      productionPathCoordinate,
			Domains:         []string{productionPathDomain},
			DomainsBySigner: map[string][]string{productionPathSigner: {productionPathDomain}},
			Interval:        time.Minute,
		})
	require.NoError(t, err)
	return reconciler
}

func productionPathBindingRecord(
	t *testing.T, service *business.Service, bindingID string,
) *business.SolutionHostBindingRecord {
	t.Helper()
	records, err := service.ListSolutionHostBindings(testCtx)
	require.NoError(t, err)
	for _, record := range records {
		if record.BindingID == bindingID {
			return record
		}
	}
	t.Fatalf("no durable record for binding %q", bindingID)
	return nil
}

// productionPathBinding is a present generation for one solution on this test
// host, with its workload in namespace.
func productionPathBinding(
	t *testing.T, solutionID, namespace string, generation uint64,
) *solutionhost.SolutionHostBinding {
	t.Helper()
	document := &solutionhost.SolutionHostBinding{
		Schema:           solutionhost.SchemaPresenceV2,
		Kind:             solutionhost.KindSolution,
		Binding:          "acme.test." + solutionID,
		Generation:       generation,
		OwnershipDomain:  productionPathDomain,
		EnvelopeRevision: solutionhost.FixtureEnvelopeRevision,
		Host: solutionhost.HostTarget{
			Coordinate: productionPathCoordinate, Component: "saas-host",
		},
		Release: solutionhost.Release{
			Publisher: "acme", Name: solutionID, Version: "1.4.0",
			Digest: solutionhost.ReleaseDigest(fixtureDigest("release/" + solutionID)),
		},
		Routes: []solutionhost.Route{{Alias: solutionID, Surface: solutionhost.SurfaceBackend}},
		Artifacts: []solutionhost.Artifact{{
			Surface: solutionhost.SurfaceBackend, Name: "api",
			Release: "acme/" + solutionID + "@1.4.0",
			Digest:  solutionhost.RenderedDigest(fixtureDigest("rendered/" + solutionID)),
		}},
		Workloads: []solutionhost.Workload{{
			Name: "api", Artifact: "api", Container: "api",
			Image: solutionhost.Image{
				Repository: "registry.example/acme/" + solutionID,
				Digest:     solutionhost.ImageDigest(fixtureDigest("image/" + solutionID)),
			},
			// The namespace the carrier is authorised against reaches the host
			// through this SPIFFE ID and nowhere else, which is what keeps it
			// inside the signed bytes.
			Identity: solutionhost.WorkloadIdentity{
				Audience: "https://test.acme.example/solutions",
				Subject:  "system:serviceaccount:" + namespace + ":api",
				SPIFFEID: "spiffe://acme.test/ns/" + namespace + "/sa/api",
			},
			NonAuthenticating: &[]string{"telemetry-sidecar"},
		}},
	}
	require.NoError(t, document.Validate(), "the fixture must be a valid binding")
	return document
}

// productionPathTombstone is the withdrawal of one binding: a higher generation
// that declares nothing at all.
func productionPathTombstone(
	t *testing.T, document *solutionhost.SolutionHostBinding, generation uint64,
) *solutionhost.SolutionHostBinding {
	t.Helper()
	removed := *document
	removed.Generation = generation
	removed.Routes, removed.Artifacts, removed.Workloads = nil, nil, nil
	removed.Modules, removed.Endpoints = nil, nil
	removed.Removed = true
	require.NoError(t, removed.Validate(), "the tombstone must be a valid binding")
	return &removed
}

// productionPathCarrier wraps a document in the carrier delivery POSTs: the
// CANONICAL bytes, which are the signing input, plus a bundle over them.
func productionPathCarrier(t *testing.T, document *solutionhost.SolutionHostBinding) []byte {
	t.Helper()
	canonical, err := document.CanonicalBytes()
	require.NoError(t, err)
	carrier, err := solutionhost.MarshalSigned(&solutionhost.Signed{
		Schema:   solutionhost.SchemaSignedV1,
		Document: canonical,
		Bundle:   json.RawMessage(solutionhost.FixtureBundle),
	})
	require.NoError(t, err)
	return carrier
}
