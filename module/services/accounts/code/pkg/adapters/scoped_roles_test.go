package adapters

import (
	"context"
	"net/http"
	"testing"
)

// TestParseScopedRoles decodes the JSON X-Scoped-Roles payload and tolerates a
// malformed one by returning nil (the header is advisory; authoritative answers
// come from CheckPermission).
func TestParseScopedRoles(t *testing.T) {
	got := parseScopedRoles(`{"module-a":["analyst"],"module-b":["admin","editor"]}`)
	want := map[string][]string{"module-a": {"analyst"}, "module-b": {"admin", "editor"}}
	if len(got) != len(want) {
		t.Fatalf("parseScopedRoles size = %d, want %d", len(got), len(want))
	}
	for scope, roles := range want {
		if len(got[scope]) != len(roles) {
			t.Fatalf("scope %q = %v, want %v", scope, got[scope], roles)
		}
	}

	if parseScopedRoles("") != nil {
		t.Error("empty payload should decode to nil")
	}
	if parseScopedRoles("{not json") != nil {
		t.Error("malformed payload should decode to nil, not error")
	}
}

// TestHasScopedRole is the header-only authorization primitive: a handler can
// decide a scoped operation from context alone, no callback to accounts.
func TestHasScopedRole(t *testing.T) {
	ctx := withScopedRoles(context.Background(), map[string][]string{
		"module-a": {"analyst"},
		"module-b": {"admin", "editor"},
	})

	if !HasScopedRole(ctx, "module-a", "analyst") {
		t.Error("expected module-a analyst grant")
	}
	if !HasScopedRole(ctx, "module-b", "editor") {
		t.Error("expected module-b editor grant")
	}
	if HasScopedRole(ctx, "module-a", "admin") {
		t.Error("module-a admin was never granted")
	}
	if HasScopedRole(ctx, "module-c", "analyst") {
		t.Error("module-c has no grants")
	}
	if HasScopedRole(context.Background(), "module-a", "analyst") {
		t.Error("empty context grants nothing")
	}
}

// TestStampForwardedHTTPIdentityCarriesScopedRoles proves a downstream service
// authorizes from the sidecar-forwarded header alone.
func TestStampForwardedHTTPIdentityCarriesScopedRoles(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-User-Id", "11111111-1111-1111-1111-111111111111")
	headers.Set("X-Org-Id", "22222222-2222-2222-2222-222222222222")
	headers.Set("X-Scoped-Roles", `{"module-a":["analyst"]}`)

	ctx := stampForwardedHTTPIdentity(context.Background(), headers)

	if !HasScopedRole(ctx, "module-a", "analyst") {
		t.Fatalf("expected scoped role from header, got %v", ScopedRolesFromContext(ctx))
	}
	if ScopedRolesTruncatedFromContext(ctx) {
		t.Fatal("no truncation header → must not report truncated")
	}
}

// TestStampForwardedHTTPIdentityCarriesTruncationSignal proves the incomplete-
// grants signal survives the header round-trip so a service knows to fall back
// to CheckPermission rather than treating an absent grant as a denial.
func TestStampForwardedHTTPIdentityCarriesTruncationSignal(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-User-Id", "11111111-1111-1111-1111-111111111111")
	headers.Set("X-Org-Id", "22222222-2222-2222-2222-222222222222")
	headers.Set("X-Scoped-Roles", `{"module-a":["analyst"]}`)
	headers.Set("X-Scoped-Roles-Truncated", "true")

	ctx := stampForwardedHTTPIdentity(context.Background(), headers)

	if !ScopedRolesTruncatedFromContext(ctx) {
		t.Fatal("expected truncation signal from header")
	}
}
