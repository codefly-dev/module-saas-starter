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
		got, err := endpointVisibility(tc.visibility, tc.location, tc.exported)
		if tc.want == "" {
			if err == nil {
				t.Errorf("accepted unsupported policy %#v as %q", tc, got)
			}
		} else if err != nil || got != tc.want {
			t.Errorf("policy %#v: got %q, %v", tc, got, err)
		}
	}
}
