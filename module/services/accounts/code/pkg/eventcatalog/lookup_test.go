package eventcatalog

import "testing"

// TestResolvePartition covers the declared ordering domains a contribution may
// write. The composed catalog only carries "{tenant_id}" today, so the
// boundary-scoped template — the finer partition EVENTS.md documents — has no
// route through the generated table and is pinned here directly. Shapes compose
// rejects (an unknown placeholder, a template with no {tenant_id}) are not
// pinned here: they cannot reach this function.
func TestResolvePartition(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
		want     string
	}{
		{name: "no declaration is no partition", template: "", want: ""},
		{name: "tenant scope", template: "{tenant_id}", want: "org-1"},
		{name: "boundary scope", template: "{tenant_id}/{boundary_id}", want: "org-1/node-7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolvePartition(tc.template, "org-1", "node-7"); got != tc.want {
				t.Fatalf("ResolvePartition(%q) = %q, want %q", tc.template, got, tc.want)
			}
		})
	}
}

// TestLookupPublishedReportsCatalogMembership pins the signal a publisher needs
// to tell "this type declares no ordering" apart from "this deployment's catalog
// has never heard of this type" — two states that must not collapse into one
// empty partition key.
func TestLookupPublishedReportsCatalogMembership(t *testing.T) {
	declared, ok := LookupPublished("scope.granted")
	if !ok {
		t.Fatal("scope.granted must be declared in the composed catalog")
	}
	if got := ResolvePartition(declared.Partition, "org-1", "node-7"); got != "org-1" {
		t.Fatalf("scope.granted partition resolved to %q, want the tenant", got)
	}
	if _, ok := LookupPublished("scope.unregistered"); ok {
		t.Fatal("an uncatalogued type must not report as declared")
	}
}

// TestUnorderedPublishedTypesSelectsUndeclaredPartitions pins the selection the
// Subscribe ordering gate depends on. The composed catalog declares a partition
// on every type today, so the list is empty — the invariant, not the length, is
// what must hold: a type is listed exactly when it declares no partition, so
// inverting the condition (and rejecting every ordered subscription, or none)
// fails here rather than in a deployment.
func TestUnorderedPublishedTypesSelectsUndeclaredPartitions(t *testing.T) {
	listed := map[string]struct{}{}
	for _, eventType := range UnorderedPublishedTypes() {
		listed[eventType] = struct{}{}
	}
	for _, e := range published {
		_, isListed := listed[e.Type]
		if declaresNone := e.Partition == ""; declaresNone != isListed {
			t.Fatalf("type %q declares partition %q but listed-as-unordered = %v", e.Type, e.Partition, isListed)
		}
	}
}
