package gitopscontract_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/codefly-dev/agents/modules/saas-starter/gitopscontract"
)

func TestInventoryEncodingIsTheSharedCanonicalBoundary(t *testing.T) {
	t.Parallel()
	inventory := gitopscontract.Inventory{
		SchemaVersion: gitopscontract.SchemaVersion,
		Module:        "users",
		Environment:   "local",
		AppProject:    "mind-users-local",
		OwnedPath:     "deployments/modules/users",
		ServiceGraph: []gitopscontract.Service{{
			Module:  "users",
			Service: "accounts",
			Path:    "services/accounts/overlays/local",
			Output: &gitopscontract.KubernetesOutput{
				Kind:            "KUSTOMIZE",
				Profile:         "KUBERNETES_OUTPUT_PROFILE_PROMOTABLE_GITOPS_V1",
				ContractVersion: "codefly.dev/kubernetes-manifest/v1",
				Validation: &gitopscontract.KubernetesValidation{
					StaticValidation:     "STATUS_PASSED",
					ServerSideValidation: "STATUS_PASSED",
					Promotable:           true,
					Violations:           []string{},
				},
			},
		}},
		Files:  []gitopscontract.InventoryFile{},
		Digest: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}

	encoded, err := gitopscontract.Encode(inventory)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := gitopscontract.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := gitopscontract.Encode(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, reencoded) {
		t.Fatalf("shared inventory encoding changed across a round trip\nfirst:\n%s\nsecond:\n%s", encoded, reencoded)
	}
}

func TestInventoryDecoderRejectsLegacyProducerSchema(t *testing.T) {
	t.Parallel()
	_, err := gitopscontract.Decode([]byte(`{
  "schemaVersion": 1,
  "module": "users",
  "environment": "local",
  "appProject": "mind-users-local",
  "ownedPath": "deployments/modules/users",
  "serviceGraph": [],
  "files": [],
  "digest": "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
}
`))
	if err == nil || !strings.Contains(err.Error(), "schema is 1, want 2") {
		t.Fatalf("Decode() error = %v, want shared schema rejection", err)
	}
}
