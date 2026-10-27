//go:build !pure

package infra_test

import (
	"crypto"
	"sync/atomic"
	"testing"

	"accounts/internal/deliverytest"
	"accounts/pkg/business"
	compositionv1 "accounts/pkg/gen/host/composition/v1"

	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// An applied generation's authority document is verified once, not on every
// check: repeated checks verify nothing more, and applying the next generation
// is what makes the next check verify again, and answer from it.
func TestAnAppliedAuthorityDocumentIsVerifiedOncePerGeneration(t *testing.T) {
	d := newProductionPathDelivery(t)
	signer := deliverytest.Signer(t, productionPathSigner)
	var verified atomic.Int32
	d.service.SetAuthorityDocumentVerifier(func(doc, envelope []byte) (string, error) {
		verified.Add(1)
		return delivery.Verify(doc, envelope, map[string]crypto.PublicKey{signer.KeyID(): signer.Public()})
	})
	reconciler, err := business.NewSolutionHostBindingReconciler(d.service, business.SolutionHostBindingReconcilerConfig{Source: business.NewDeliveredSolutionHostBindings(d.service), Verifier: productionVerifier(t), Coordinate: productionPathCoordinate})
	require.NoError(t, err)
	d.service.SetCompositionReconcile(reconciler.RunOnce)
	m := productionPathManifest(t, uniqueAlias("cached-policy"), 1)
	// A module: the policy pair is what is under test, and a solution would
	// have to declare its roles (an installer) for the composition to take it.
	m.Kind = solutionhost.KindModule
	m.Authority = &solutionhost.Authority{Principal: m.Alias, ResourceKinds: []string{"example"}}
	d.callerNamespace = productionPathNamespace(m.Alias)
	apply := func(method string) {
		t.Helper()
		raw, err := solutionhost.SignedPayload(m)
		require.NoError(t, err)
		doc, err := proto.Marshal(&compositionv1.AuthorityManifestV1{Module: m.Alias, Generation: m.Generation, CoreManifestDigest: delivery.Digest(raw), Methods: []*compositionv1.MethodPolicyV1{{Method: method, AnyCheckedCaller: true}}})
		require.NoError(t, err)
		envelope, err := delivery.Sign(doc, signer.KeyID(), signer.Sign)
		require.NoError(t, err)
		_, err = d.service.ApplyComposition(testCtx, &compositionv1.ApplyManifestRequest{
			Carrier:    &compositionv1.SignedManifestV1{Document: raw, Envelope: deliverytest.Envelope(t, raw, productionPathSigner)},
			Authority:  &compositionv1.SignedManifestV1{Document: doc, Envelope: envelope},
			Credential: "projected-delivery-token",
		})
		require.NoError(t, err)
	}

	apply("/example.Service/Read")
	require.NoError(t, d.service.CheckDeliveredMethod(testCtx, m.Alias, "/example.Service/Read", nil))
	once := verified.Load()
	for range 3 {
		require.NoError(t, d.service.CheckDeliveredMethod(testCtx, m.Alias, "/example.Service/Read", nil))
	}
	require.Equal(t, once, verified.Load(), "repeated checks of one generation verify nothing more")

	m.Generation++
	apply("/example.Service/Write")
	before := verified.Load()
	require.ErrorIs(t, d.service.CheckDeliveredMethod(testCtx, m.Alias, "/example.Service/Read", nil), business.ErrDeliveredMethodUnknown)
	require.NoError(t, d.service.CheckDeliveredMethod(testCtx, m.Alias, "/example.Service/Write", nil))
	require.Equal(t, before+1, verified.Load(), "the new generation is verified once, and answers")
}
