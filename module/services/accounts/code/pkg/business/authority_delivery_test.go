package business

import (
	"accounts/internal/deliverytest"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	"context"
	"crypto"
	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestAuthorityDocumentMustBeTheSignedPair(t *testing.T) {
	manifest := fixtureManifest(t, "example", 1)
	raw, err := solutionhost.SignedPayload(manifest)
	require.NoError(t, err)
	core, err := solutionhost.VerifyDelivered(context.Background(), &solutionhost.Signed{Document: raw, Envelope: deliverytest.Envelope(t, raw, testDeliveryKey)}, fixtureSigner(t))
	require.NoError(t, err)
	signer := deliverytest.Signer(t, testDeliveryKey)
	other := deliverytest.Signer(t, "other-release")
	svc := &Service{}
	svc.SetAuthorityDocumentVerifier(func(doc, envelope []byte) (string, error) {
		return delivery.Verify(doc, envelope, map[string]crypto.PublicKey{signer.KeyID(): signer.Public(), other.KeyID(): other.Public()})
	})
	doc := &compositionv1.AuthorityManifestV1{Module: manifest.Alias, Generation: 1, CoreManifestDigest: delivery.Digest(raw), Methods: []*compositionv1.MethodPolicyV1{{Method: "/example.Service/Read", AnyCheckedCaller: true}}}
	for _, tc := range []struct {
		name           string
		edit           func(*compositionv1.AuthorityManifestV1)
		other, corrupt bool
	}{
		{name: "valid"}, {name: "module", edit: func(d *compositionv1.AuthorityManifestV1) { d.Module = "other" }},
		{name: "generation", edit: func(d *compositionv1.AuthorityManifestV1) { d.Generation++ }},
		{name: "digest", edit: func(d *compositionv1.AuthorityManifestV1) { d.CoreManifestDigest = "sha256:another" }},
		{name: "signer", other: true}, {name: "signature", corrupt: true},
		{name: "duplicate", edit: func(d *compositionv1.AuthorityManifestV1) { d.Methods = append(d.Methods, d.Methods[0]) }},
		{name: "contradiction", edit: func(d *compositionv1.AuthorityManifestV1) {
			d.Methods[0].Requires = []*authorityv1.WorkScopeV1{{ResourceKind: "example", Actions: []string{"read"}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := proto.CloneOf(doc)
			if tc.edit != nil {
				tc.edit(candidate)
			}
			payload, err := proto.Marshal(candidate)
			require.NoError(t, err)
			key := signer
			if tc.other {
				key = other
			}
			envelope, err := delivery.Sign(payload, key.KeyID(), key.Sign)
			require.NoError(t, err)
			if tc.corrupt {
				payload = append(payload, 0)
			}
			_, err = svc.verifyAuthorityDocument(&compositionv1.SignedManifestV1{Document: payload, Envelope: envelope}, core)
			if tc.name == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestMethodPolicyRequiresWholeKindAuthority(t *testing.T) {
	doc := &compositionv1.AuthorityManifestV1{Methods: []*compositionv1.MethodPolicyV1{
		{Method: "/example.Service/Read", Requires: []*authorityv1.WorkScopeV1{{ResourceKind: "example", Actions: []string{"read"}}}},
		{Method: "GET /records/{id}", AnyCheckedCaller: true}, {Method: "/example.Service/Unmarked"},
	}}
	held := []*basev0.WorkScopeV1{{ResourceKind: "example", Actions: []string{"read"}}}
	require.NoError(t, checkMethodPolicy(doc, "example", "/example.Service/Read", held))
	require.NoError(t, checkMethodPolicy(doc, "example", "POST /example.Service/Read", held))
	require.NoError(t, checkMethodPolicy(doc, "example", "GET /records/one", held))
	require.ErrorIs(t, checkMethodPolicy(doc, "example", "/example.Service/Missing", held), ErrDeliveredMethodUnknown)
	require.ErrorContains(t, checkMethodPolicy(doc, "example", "/example.Service/Unmarked", held), "Unmarked")
	held[0].ResourceIds = []string{"one"}
	require.ErrorContains(t, checkMethodPolicy(doc, "example", "/example.Service/Read", held), "does not hold")
	doc.Methods = append(doc.Methods, &compositionv1.MethodPolicyV1{Method: "GET /records/{another}", AnyCheckedCaller: true})
	require.ErrorContains(t, checkMethodPolicy(doc, "example", "GET /records/one", held), "more than one")
}

func TestHTTPPolicyMatchesPublishedRouteTemplates(t *testing.T) {
	for _, tc := range []struct {
		declared, request string
		matches           bool
	}{
		{"POST /v1/example/{path=**}", "POST /v1/example/service/Read", true},
		{"POST /v1/example/{path=**}", "POST /v1/example/Read", true},
		{"POST /v1/example/{path=**}", "POST /v1/example/", false},
		{"POST /v1/example/{path=**}", "GET /v1/example/service/Read", false},
		{"POST /v1/example/{path=**}", "POST /v1/other/service/Read", false},
		{"POST /v1/example/{path=**}:settle", "POST /v1/example/one/two:settle", true},
		{"POST /v1/example/{path=**}:settle", "POST /v1/example/one/two", false},
		{"POST /v1/example/{path=**}:settle", "POST /v1/example/:settle", false},
		{"POST /v1/example/{id}:settle", "POST /v1/example/one:settle", true},
		{"POST /v1/example/{id}:settle", "POST /v1/example/:settle", false},
		{"POST /v1/example/{id}:settle", "POST /v1/example/one:delete", false},
		{"POST /v1/example/{id}:settle", "POST /v1/example/one/two:settle", false},
		{"GET /v1/example/{name}.{ext}", "GET /v1/example/one.json", true},
		{"GET /v1/example/{name}.{ext}", "GET /v1/example/one.", false},
		{"GET /v1/example/{path=**}/tail", "GET /v1/example/one/tail", false},
		{"GET /v1/example/=**}", "GET /v1/example/one", false},
		{"GET /v1/example/{}", "GET /v1/example/one", false},
	} {
		t.Run(tc.declared+" against "+tc.request, func(t *testing.T) {
			require.Equal(t, tc.matches, methodRouteMatches(tc.declared, tc.request))
		})
	}
	doc := &compositionv1.AuthorityManifestV1{Methods: []*compositionv1.MethodPolicyV1{
		{Method: "POST /v1/example/{path=**}", AnyCheckedCaller: true},
		{Method: "POST /v1/example/{service}/{method}", AnyCheckedCaller: true},
	}}
	require.ErrorContains(t, checkMethodPolicy(doc, "example", "POST /v1/example/service/Read", nil), "more than one declared route")
}
