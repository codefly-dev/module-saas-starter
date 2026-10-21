package business

import "testing"

// A delegation is a fact about a PERSON and a SOURCE. A view that cannot see
// its binding says nothing durable about that fact: the organisation's consent
// may not carry the binding, or the registry may have been replaced mid-call.
//
// Observed on a deployed cell, 2026-10-18: a delegation was revoked as
// "binding_changed" NINE SECONDS after it was recorded, with a binding digest
// identical to the one stored — because recording read the raw registry and the
// mint read the consent-narrowed one. Every retry recreated it and the next
// mint destroyed it again.
func TestABindingThisViewCannotSeeDoesNotRevokeTheDelegation(t *testing.T) {
	binding := ModuleOperationAudience{
		Audience:               "runtime",
		InvokeScopes:           []ModuleOperationScope{{ResourceKind: "documents.ingest", Actions: []string{"read", "write"}}},
		SourceDelegationScopes: []ModuleOperationScope{{ResourceKind: "documents.ingest", Actions: []string{"read", "write"}}},
	}
	delegation := &SourceDelegation{
		BindingID:     "runtime-admission",
		BindingDigest: SourceDelegationBindingDigest(binding),
	}

	t.Run("declared and unchanged is OK", func(t *testing.T) {
		grant := ModulePrincipalGrant{Prefix: "documents", OperationAudiences: map[string]ModuleOperationAudience{"runtime-admission": binding}}
		if _, state := grant.currentSourceDelegationBinding(delegation); state != sourceDelegationBindingOK {
			t.Fatalf("state = %v, want OK", state)
		}
	})

	t.Run("absent from this view is UNAVAILABLE, never changed", func(t *testing.T) {
		// Exactly the consent-narrowed grant that destroyed the delegation.
		grant := ModulePrincipalGrant{Prefix: "documents", OperationAudiences: map[string]ModuleOperationAudience{
			"model": {Audience: "modelservice"},
		}}
		_, state := grant.currentSourceDelegationBinding(delegation)
		if state == sourceDelegationBindingChanged {
			t.Fatal("a binding missing from a narrowed view must not read as changed: that revokes the person's grant")
		}
		if state != sourceDelegationBindingUnavailable {
			t.Fatalf("state = %v, want Unavailable", state)
		}
	})

	t.Run("an empty view is UNAVAILABLE", func(t *testing.T) {
		// A registry replaced mid-call, or fenced behind its envelope revision.
		grant := ModulePrincipalGrant{Prefix: "documents"}
		if _, state := grant.currentSourceDelegationBinding(delegation); state != sourceDelegationBindingUnavailable {
			t.Fatalf("state = %v, want Unavailable", state)
		}
	})

	t.Run("declared but carrying no delegation scopes is UNAVAILABLE", func(t *testing.T) {
		noScopes := binding
		noScopes.SourceDelegationScopes = nil
		grant := ModulePrincipalGrant{Prefix: "documents", OperationAudiences: map[string]ModuleOperationAudience{"runtime-admission": noScopes}}
		if _, state := grant.currentSourceDelegationBinding(delegation); state != sourceDelegationBindingUnavailable {
			t.Fatalf("state = %v, want Unavailable", state)
		}
	})

	t.Run("declared and genuinely different is CHANGED, and revokes", func(t *testing.T) {
		// The one durable answer: the module still offers this binding and what
		// the person delegated has moved under them.
		moved := binding
		moved.SourceDelegationScopes = []ModuleOperationScope{{ResourceKind: "documents.ingest", Actions: []string{"read"}}}
		grant := ModulePrincipalGrant{Prefix: "documents", OperationAudiences: map[string]ModuleOperationAudience{"runtime-admission": moved}}
		if _, state := grant.currentSourceDelegationBinding(delegation); state != sourceDelegationBindingChanged {
			t.Fatalf("state = %v, want Changed", state)
		}
	})

	t.Run("a changed audience is CHANGED", func(t *testing.T) {
		moved := binding
		moved.Audience = "somewhere-else"
		grant := ModulePrincipalGrant{Prefix: "documents", OperationAudiences: map[string]ModuleOperationAudience{"runtime-admission": moved}}
		if _, state := grant.currentSourceDelegationBinding(delegation); state != sourceDelegationBindingChanged {
			t.Fatalf("state = %v, want Changed", state)
		}
	})
}
