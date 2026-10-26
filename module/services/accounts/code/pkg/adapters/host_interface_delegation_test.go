package adapters

import (
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
)

// What a delegation leaves of what the owner holds is the meet, kind by kind:
// never an action or a resource either side lacks, and a kind only one names
// is gone.
func TestADelegationCutsWhatTheOwnerHoldsKindByKind(t *testing.T) {
	held := []*basev0.WorkScopeV1{
		hopScope("example-records", []string{"invoke", "read"}),
		hopScope("tool.items", []string{"read"}),
	}
	delegated := []*basev0.WorkScopeV1{
		hopScope("example-records", []string{"read", "write"}, "a"),
		hopScope("modelservice.profiles", []string{"invoke"}),
	}
	requireScopes(t, []*basev0.WorkScopeV1{hopScope("example-records", []string{"read"}, "a")}, intersectScopes(held, delegated))
	if got := intersectScopes(held, []*basev0.WorkScopeV1{hopScope("example-records", []string{"write"})}); len(got) != 0 {
		t.Fatalf("a delegation of what the owner does not hold leaves %v", got)
	}
}
