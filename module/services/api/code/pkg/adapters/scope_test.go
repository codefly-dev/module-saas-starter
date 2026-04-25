package adapters

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestScopeMatches pins the wildcard semantics: `*:*` covers every
// scope, `users:*` covers any user action, `*:read` covers any read.
// Required strings are matched literally; wildcards in the required
// argument don't expand. These rules are documented contract — every
// API-key consumer relies on them.
func TestScopeMatches(t *testing.T) {
	cases := []struct {
		granted, required string
		want              bool
	}{
		// exact match
		{"users:read", "users:read", true},
		{"users:write", "users:read", false},

		// resource wildcard
		{"users:*", "users:read", true},
		{"users:*", "users:write", true},
		{"users:*", "orgs:read", false},

		// action wildcard
		{"*:read", "users:read", true},
		{"*:read", "orgs:read", true},
		{"*:read", "users:write", false},

		// full wildcard
		{"*:*", "users:read", true},
		{"*:*", "anything:goes", true},

		// malformed → no match
		{"bare", "users:read", false},
		{"users:read:extra", "users:read", false},
	}
	for _, tt := range cases {
		got := scopeMatches(tt.granted, tt.required)
		if got != tt.want {
			t.Errorf("scopeMatches(%q, %q) = %v, want %v",
				tt.granted, tt.required, got, tt.want)
		}
	}
}

// TestRequireScope_NoScopesPassesThrough — the auth interceptor
// stamps no scopes on the context for JWT-authenticated callers
// (interactive sessions). RBAC has already gated those, so the
// scope check must be a no-op rather than a deny — otherwise every
// JWT user would be locked out of every scoped handler.
func TestRequireScope_NoScopesPassesThrough(t *testing.T) {
	ctx := context.Background()
	if err := requireScope(ctx, "users:write"); err != nil {
		t.Fatalf("requireScope on empty ctx should pass-through, got %v", err)
	}
}

// TestRequireScope_GrantedScopeAllows — a key with `users:write` can
// hit a handler requiring exactly that scope.
func TestRequireScope_GrantedScopeAllows(t *testing.T) {
	ctx := withScopes(context.Background(), []string{"users:write"})
	if err := requireScope(ctx, "users:write"); err != nil {
		t.Fatalf("granted scope should allow, got %v", err)
	}
}

// TestRequireScope_WildcardCovers — a key with `*:*` can hit any
// scoped handler. Same deal for tighter wildcards.
func TestRequireScope_WildcardCovers(t *testing.T) {
	for _, granted := range []string{"*:*", "users:*", "*:write"} {
		ctx := withScopes(context.Background(), []string{granted})
		if err := requireScope(ctx, "users:write"); err != nil {
			t.Errorf("granted %q should cover users:write, got %v", granted, err)
		}
	}
}

// TestRequireScope_MissingDenied — a key with only `users:read`
// trying a `users:write` handler must be rejected with
// PermissionDenied.
func TestRequireScope_MissingDenied(t *testing.T) {
	ctx := withScopes(context.Background(), []string{"users:read"})
	err := requireScope(ctx, "users:write")
	if err == nil {
		t.Fatal("missing scope should deny, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %s", st.Code())
	}
}
