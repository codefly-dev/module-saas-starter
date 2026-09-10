package eventcatalog

import "testing"

// TestResolvePartition covers the declared ordering domains a contribution may
// write. The composed catalog only carries "{tenant_id}" today, so the
// boundary-scoped template — the finer partition EVENTS.md documents — has no
// route through the generated table and is pinned here directly.
func TestResolvePartition(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
		want     string
	}{
		{name: "no declaration is no partition", template: "", want: ""},
		{name: "tenant scope", template: "{tenant_id}", want: "org-1"},
		{name: "boundary scope", template: "{tenant_id}/{boundary_id}", want: "org-1/node-7"},
		{name: "literal prefix is preserved", template: "scope/{boundary_id}", want: "scope/node-7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolvePartition(tc.template, "org-1", "node-7"); got != tc.want {
				t.Fatalf("resolvePartition(%q) = %q, want %q", tc.template, got, tc.want)
			}
		})
	}
}

// TestPartitionKeyUnknownTypeTakesNoPartition proves the catalog is the only
// authority on ordering: a type it does not carry resolves to the empty key, so
// publish_domain_event takes no advisory lock for it.
func TestPartitionKeyUnknownTypeTakesNoPartition(t *testing.T) {
	if got := PartitionKey("scope.unregistered", "org-1", "node-7"); got != "" {
		t.Fatalf("an uncatalogued type must take no partition, got %q", got)
	}
	if got := PartitionKey("scope.granted", "org-1", "node-7"); got != "org-1" {
		t.Fatalf("a declared {tenant_id} partition must resolve to the tenant, got %q", got)
	}
}
