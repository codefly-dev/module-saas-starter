package cataloggen

import "testing"

// The OTHER HALF OF A PIN. The module agent at the repository root holds the same
// projection as this package's manifestCatalogVisibility (deployment_model.go),
// in topology_manifests.go and a separate Go module
// so neither side can import the other, and topology_visibility_test.go carries the
// matching table.
//
// This test exists because the two drifted and the symptom was remote from the
// cause: when the manifests stopped authoring `allow-modules: ["*"]`, this copy
// accepted the new spelling and the root copy still demanded the wildcard, so the
// red surfaced as TestShippedModuleAuthorityPortIsAllocatedNotDeclared failing in
// the `Codefly SDK boundary` gate — a test about PORTS, on a manifest this package
// was perfectly happy with. A row added here is owed to the root table too.
func TestManifestCatalogVisibilityMatchesTheRootProjection(t *testing.T) {
	for _, tc := range []struct {
		visibility, location string
		exported             bool
		want                 string
	}{
		{"internal", "", false, "module"},
		{"private", "external", false, "external"},
		{"public", "", false, "public"},
		{"", "", false, "private"},
		{"internal", "", true, "module"},
		{"module", "", false, ""},
		{"external", "", false, ""},
		{"private", "elsewhere", false, ""},
		{"internal", "external", false, ""},
	} {
		got, err := manifestCatalogVisibility(tc.visibility, tc.location, tc.exported)
		if tc.want == "" {
			if err == nil {
				t.Errorf("accepted unsupported policy %#v as %q", tc, got)
			}
		} else if err != nil || got != tc.want {
			t.Errorf("policy %#v: got %q, %v", tc, got, err)
		}
	}
}
