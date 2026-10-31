package business

import (
	"testing"

	compositionv1 "accounts/pkg/gen/host/composition/v1"
	modulecontract "github.com/codefly-dev/core/contracts/module"
	"github.com/stretchr/testify/require"
)

func TestDocumentOfRetainsSetupAndOwnerAwareKinds(t *testing.T) {
	in := &compositionv1.AuthorityManifestV1{
		OwnerAwareResourceKinds: []string{"example.items"},
		Setup:                   []*compositionv1.SetupGrantV1{{Binding: "records", Methods: []string{"/records.v1.Schema/Define"}, Scopes: []string{"records.types:define"}}},
	}
	got := documentOf(in)
	require.Equal(t, in.OwnerAwareResourceKinds, got.OwnerAwareResourceKinds)
	require.Equal(t, []modulecontract.ResolvedSetup{{Binding: "records", Methods: []string{"/records.v1.Schema/Define"}, Scopes: []string{"records.types:define"}}}, got.Setup)
}
