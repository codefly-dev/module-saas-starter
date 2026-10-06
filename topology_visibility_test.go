package main

import "testing"

func TestEndpointCatalogProjectionPreservesOrRefusesPolicy(t *testing.T) {
	for _, tc := range []struct {
		visibility, location string
		allowed              []string
		want                 string
	}{
		{"internal", "", []string{"*"}, "module"},
		{"private", "external", nil, "external"},
		{"public", "", nil, "public"},
		{"", "", nil, "private"},
		{"internal", "", []string{"example"}, ""},
		{"internal", "", nil, ""},
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
