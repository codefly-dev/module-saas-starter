package fixtures

import (
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
)

// A stored approval is reused while the core holds the installation pending
// or active (a replay of the same operation is idempotent, and a fresh
// operation id against an active record is refused by the host's plan
// guard). After an uninstall the receipt is refused for good, and a fixture
// that kept presenting it never installed again.
func TestAStoredApprovalContinuesAPendingOrActiveAttempt(t *testing.T) {
	for name, tc := range map[string]struct {
		all  []domain.Installation
		want bool
	}{
		"pending":       {all: []domain.Installation{{ID: "i", Status: domain.Pending}}, want: true},
		"active":        {all: []domain.Installation{{ID: "i", Status: domain.Active}}, want: true},
		"revoked":       {all: []domain.Installation{{ID: "i", Status: domain.Revoked}}, want: false},
		"another's":     {all: []domain.Installation{{ID: "other", Status: domain.Pending}}, want: false},
		"not installed": {want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := attemptContinues(tc.all, "i"); got != tc.want {
				t.Fatalf("attemptContinues = %v, want %v", got, tc.want)
			}
		})
	}
}
