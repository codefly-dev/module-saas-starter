package main

import "testing"

// This table is one half of a PIN. The shipped generator holds the same
// projection as manifestCatalogVisibility
// (module/services/accounts/code/pkg/cataloggen/deployment_model.go) in a separate
// Go module, so neither side can import the other and only matching tables keep
// them honest. When they drifted — this copy still demanding
// `allow-modules: ["*"]` after the manifests stopped authoring it —
// TestShippedModuleAuthorityPortIsAllocatedNotDeclared went red on a manifest the
// other copy accepted. A row added here is owed to the accounts table too.
func TestEndpointCatalogProjectionPreservesOrRefusesPolicy(t *testing.T) {
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
		got, err := endpointVisibility(tc.visibility, tc.location, tc.allowed)
		if tc.want == "" {
			if err == nil {
				t.Errorf("accepted unsupported policy %#v as %q", tc, got)
			}
		} else if err != nil || got != tc.want {
			t.Errorf("policy %#v: got %q, %v", tc, got, err)
		}
	}
}
