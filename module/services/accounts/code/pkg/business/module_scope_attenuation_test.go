package business

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The scopes a manifest asks for and the envelope does not carry are what the
// reconcile pass used to drop in silence. attenuated is that difference, and these
// cases fix the shapes that mattered on 2026-11-10.
func TestAttenuatedNamesEveryScopeTheEnvelopeDoesNotCarry(t *testing.T) {
	asked := func(scopes ...string) map[string]bool {
		set := map[string]bool{}
		for _, scope := range scopes {
			set[scope] = true
		}
		return set
	}

	for _, tc := range []struct {
		name   string
		asked  map[string]bool
		kept   []ModuleOperationScope
		expect []string
	}{
		{
			// The one that cost a day: the binding keeps most of what it asked
			// for, so it still reads as granted, and one action is gone.
			name:  "one action of a kept kind",
			asked: asked("robin.conversations:read", "robin.conversations:write", "robin.conversations:delete"),
			kept: []ModuleOperationScope{
				{ResourceKind: "robin.conversations", Actions: []string{"read", "write"}},
			},
			expect: []string{"robin.conversations:delete"},
		},
		{
			// Nothing intersected, so validateOperationAudiences will omit the
			// binding; these are the only lines that name why.
			name:   "the whole arm",
			asked:  asked("records.types:define"),
			kept:   nil,
			expect: []string{"records.types:define"},
		},
		{
			name:  "a kind the envelope omits entirely, beside one it carries",
			asked: asked("documents:read", "documents.collection:create"),
			kept: []ModuleOperationScope{
				{ResourceKind: "documents", Actions: []string{"read"}},
			},
			expect: []string{"documents.collection:create"},
		},
		{
			name:  "granted whole",
			asked: asked("documents:read", "documents:write"),
			kept: []ModuleOperationScope{
				{ResourceKind: "documents", Actions: []string{"read", "write"}},
			},
			expect: nil,
		},
		{
			// An envelope wider than the ask is not attenuation; the parity gate
			// answers that one, in the other direction.
			name:  "the envelope carries more than was asked",
			asked: asked("documents:read"),
			kept: []ModuleOperationScope{
				{ResourceKind: "documents", Actions: []string{"read", "write"}},
			},
			expect: nil,
		},
		{
			name:   "an operation the manifest does not declare",
			asked:  nil,
			kept:   nil,
			expect: nil,
		},
		{
			name:   "sorted, so the log line is stable",
			asked:  asked("b.kind:z", "a.kind:y", "a.kind:x"),
			kept:   nil,
			expect: []string{"a.kind:x", "a.kind:y", "b.kind:z"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expect, attenuated(tc.asked, tc.kept))
		})
	}
}
