package business

import (
	"context"
	"crypto"
	"testing"

	"accounts/internal/deliverytest"
	compositionv1 "accounts/pkg/gen/host/composition/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	modulecontract "github.com/codefly-dev/core/contracts/module"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// The host's generated protobuf adapter must preserve both signed declarations.
// All negative cases have valid signatures, identity and digest pairing.
func TestAccountsInstallationDeclarationsSurviveBothSignedCarriers(t *testing.T) {
	for _, change := range []string{"equal", "selector", "selector omitted", "edge identity", "edge presence", "edge order", "edges omitted", "sidecar omitted", "legacy empty"} {
		t.Run(change, func(t *testing.T) {
			manifest := fixtureManifest(t, "example", 3)
			manifest.Authority = &solutionhost.Authority{Principal: "module:example", InstallationRole: "none", EdgeDependencies: []modulecontract.EdgeDependency{{Module: "module:records", Presence: "required"}, {Module: "module:search", Presence: "optional"}}}
			if change == "legacy empty" {
				manifest.Authority.InstallationRole = ""
				manifest.Authority.EdgeDependencies = nil
			}
			raw, err := solutionhost.SignedPayload(manifest)
			require.NoError(t, err)
			signer := deliverytest.Signer(t, testDeliveryKey)
			signedCore := &solutionhost.Signed{Document: raw, Envelope: deliverytest.Envelope(t, raw, testDeliveryKey)}
			verified, err := solutionhost.VerifyDelivered(context.Background(), signedCore, fixtureSigner(t))
			require.NoError(t, err)
			document := &compositionv1.AuthorityManifestV1{Module: manifest.Alias, Generation: manifest.Generation, CoreManifestDigest: delivery.Digest(raw), InstallationRole: manifest.Authority.InstallationRole}
			for _, edge := range manifest.Authority.EdgeDependencies {
				document.EdgeDependencies = append(document.EdgeDependencies, &compositionv1.EdgeDependencyV1{Module: edge.Module, Presence: edge.Presence})
			}
			switch change {
			case "selector":
				document.InstallationRole = "reader"
			case "selector omitted":
				document.InstallationRole = ""
			case "edge identity":
				document.EdgeDependencies[0].Module = "records-route"
			case "edge presence":
				document.EdgeDependencies[0].Presence = "optional"
			case "edge order":
				document.EdgeDependencies[0], document.EdgeDependencies[1] = document.EdgeDependencies[1], document.EdgeDependencies[0]
			case "edges omitted":
				document.EdgeDependencies = nil
			case "legacy empty":
				document.EdgeDependencies = []*compositionv1.EdgeDependencyV1{}
			}
			payload, err := proto.Marshal(document)
			require.NoError(t, err)
			envelope, err := delivery.Sign(payload, signer.KeyID(), signer.Sign)
			require.NoError(t, err)
			signed := &compositionv1.SignedManifestV1{Document: payload, Envelope: envelope}
			svc := &Service{}
			svc.SetAuthorityDocumentVerifier(func(doc, envelope []byte) (string, error) {
				return delivery.Verify(doc, envelope, map[string]crypto.PublicKey{signer.KeyID(): signer.Public()})
			})
			_, err = svc.verifyAuthorityDocument(signed, verified)
			require.NoError(t, err, "both signatures and pair identity must be valid")
			if change == "sidecar omitted" {
				signed = nil
			}
			got, err := coreModuleOf(manifest, raw, signed)
			if change == "equal" || change == "legacy empty" {
				require.NoError(t, err)
				require.Equal(t, document.InstallationRole, got.InstallationRole)
				require.Len(t, got.EdgeDependencies, len(document.EdgeDependencies))
				for i, edge := range document.EdgeDependencies {
					require.Equal(t, domain.EdgeDependency{Module: edge.Module, Presence: edge.Presence}, got.EdgeDependencies[i])
				}
				return
			}
			require.Error(t, err)
			code, ok := domain.CodeOf(err)
			require.True(t, ok)
			require.Equal(t, domain.Denied, code)
			// ApplyComposition must refuse before it tries to reach a store at all.
			svc.SetCompositionReconcile(func(context.Context) error { t.Fatal("mismatch reached reconcile"); return nil })
			_, err = svc.ApplyComposition(context.Background(), &compositionv1.ApplyManifestRequest{Carrier: &compositionv1.SignedManifestV1{Document: raw, Envelope: signedCore.Envelope}, Authority: signed})
			require.ErrorIs(t, err, ErrSolutionDeliveryUnattested)
		})
	}
}
