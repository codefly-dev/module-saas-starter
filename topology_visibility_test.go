package main

import "testing"

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
