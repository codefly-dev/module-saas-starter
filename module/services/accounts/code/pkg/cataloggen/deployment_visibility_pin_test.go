package cataloggen

import "testing"

// The OTHER HALF OF A PIN. The module agent at the repository root holds the same
// projection as endpointVisibility (topology_manifests.go), in a separate Go module
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
		allowed              []string
		want                 string
	}{
		// `internal` with NO allow-list is what the manifests author at core
		// v0.14.0, and it maps to the same category the legacy wildcard did.
		{"internal", "", nil, "module"},
		{"internal", "", []string{}, "module"},
		{"internal", "", []string{"*"}, "module"},
		{"private", "external", nil, "external"},
		{"public", "", nil, "public"},
		{"", "", nil, "private"},
		// A NARROWER list still refuses: this catalog cannot express "some
		// modules", so admitting one would silently widen the generated policy to
		// every module.
		{"internal", "", []string{"example"}, ""},
		{"internal", "", []string{"*", "example"}, ""},
		{"public", "", []string{"example"}, ""},
		{"module", "", nil, ""},
		{"external", "", nil, ""},
		{"private", "elsewhere", nil, ""},
		{"internal", "external", []string{"example"}, ""},
	} {
		got, err := manifestCatalogVisibility(tc.visibility, tc.location, tc.allowed)
		if tc.want == "" {
			if err == nil {
				t.Errorf("accepted unsupported policy %#v as %q", tc, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("policy %#v: got %q, %v", tc, got, err)
		}
	}
}
