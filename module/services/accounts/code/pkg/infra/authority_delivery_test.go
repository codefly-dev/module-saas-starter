//go:build !pure

package infra_test

import (
	"accounts/internal/deliverytest"
	"accounts/pkg/business"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	"context"
	"crypto"
	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestCompositionPersistsOneImmutablePolicyPair(t *testing.T) {
	d := newProductionPathDelivery(t)
	signer := deliverytest.Signer(t, productionPathSigner)
	d.service.SetAuthorityDocumentVerifier(func(doc, envelope []byte) (string, error) {
		return delivery.Verify(doc, envelope, map[string]crypto.PublicKey{signer.KeyID(): signer.Public()})
	})
	reconciler, err := business.NewSolutionHostBindingReconciler(d.service, business.SolutionHostBindingReconcilerConfig{Source: business.NewDeliveredSolutionHostBindings(d.service), Verifier: productionVerifier(t), Coordinate: productionPathCoordinate})
	require.NoError(t, err)
	d.service.SetCompositionReconcile(reconciler.RunOnce)
	m := productionPathManifest(t, uniqueAlias("policy-pair"), 1)
	m.Authority = &solutionhost.Authority{Principal: m.Alias, ResourceKinds: []string{"example"}}
	d.callerNamespace = productionPathNamespace(m.Alias)
	request := func(m *solutionhost.ModuleManifest, method string) *compositionv1.ApplyManifestRequest {
		raw, err := solutionhost.SignedPayload(m)
		require.NoError(t, err)
		req := &compositionv1.ApplyManifestRequest{Carrier: &compositionv1.SignedManifestV1{Document: raw, Envelope: deliverytest.Envelope(t, raw, productionPathSigner)}, Credential: "projected-delivery-token"}
		if method != "" {
			doc, err := proto.Marshal(&compositionv1.AuthorityManifestV1{Module: m.Alias, Generation: m.Generation, CoreManifestDigest: delivery.Digest(raw), Methods: []*compositionv1.MethodPolicyV1{{Method: method, AnyCheckedCaller: true}}})
			require.NoError(t, err)
			envelope, err := delivery.Sign(doc, signer.KeyID(), signer.Sign)
			require.NoError(t, err)
			req.Authority = &compositionv1.SignedManifestV1{Document: doc, Envelope: envelope}
		}
		return req
	}
	first := request(m, "/example.Service/Read")
	_, err = d.service.ApplyComposition(testCtx, first)
	require.NoError(t, err)
	_, err = d.service.ApplyComposition(testCtx, first)
	require.NoError(t, err, "exact replay")
	require.NoError(t, d.service.CheckDeliveredMethod(testCtx, m.Alias, "/example.Service/Read", nil))
	_, err = d.service.ApplyComposition(testCtx, request(m, "/example.Service/Write"))
	require.ErrorIs(t, err, business.ErrSolutionDeliveryConflict)
	_, err = d.service.ApplyComposition(testCtx, request(m, ""))
	require.ErrorIs(t, err, business.ErrSolutionDeliveryConflict, "cannot strip policy on replay")
	require.NoError(t, d.service.CheckDeliveredMethod(testCtx, m.Alias, "/example.Service/Read", nil))
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		row, err := testStore.ReadDeliveredDocument(ctx, m.Binding, m.Generation)
		require.NoError(t, err)
		require.Equal(t, first.Authority.Document, row.AuthorityDocument)
		require.Equal(t, first.Authority.Envelope, row.AuthorityEnvelope)
		return nil
	}))
	// A newer receipt alone cannot replace the applied policy.
	m.Generation++
	second := request(m, "")
	raw, err := solutionhost.MarshalSigned(&solutionhost.Signed{Document: second.Carrier.Document, Envelope: second.Carrier.Envelope})
	require.NoError(t, err)
	_, _, err = d.service.ReceiveSolutionDelivery(testCtx, raw, "Bearer "+second.Credential)
	require.NoError(t, err)
	require.NoError(t, d.service.CheckDeliveredMethod(testCtx, m.Alias, "/example.Service/Read", nil))
	_, err = d.service.ApplyComposition(testCtx, second)
	require.NoError(t, err)
	require.ErrorContains(t, d.service.CheckDeliveredMethod(testCtx, m.Alias, "/example.Service/Read", nil), "no method policy")
	_, err = d.service.ApplyComposition(testCtx, first)
	require.ErrorIs(t, err, business.ErrSolutionDeliveryConflict)
	d.callerAccount = "api"
	m.Generation++
	_, err = d.service.ApplyComposition(testCtx, request(m, "/example.Service/Read"))
	require.Error(t, err, "release signature alone does not authenticate a carrier")
}
