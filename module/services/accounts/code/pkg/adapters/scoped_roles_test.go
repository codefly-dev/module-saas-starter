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
// decide a scoped operation from context alone, no callback to accounts. A miss
// is conclusive unless the grants were truncated.
func TestHasScopedRole(t *testing.T) {
	ctx := withScopedRoles(context.Background(), map[string][]string{
		"module-a": {"analyst"},
		"module-b": {"admin", "editor"},
	})

	assertScopedRole := func(scope, role string, wantGranted, wantConclusive bool) {
		t.Helper()
		granted, conclusive := HasScopedRole(ctx, scope, role)
		if granted != wantGranted || conclusive != wantConclusive {
			t.Errorf("HasScopedRole(%q,%q) = (%v,%v), want (%v,%v)",
				scope, role, granted, conclusive, wantGranted, wantConclusive)
		}
	}

	assertScopedRole("module-a", "analyst", true, true)
	assertScopedRole("module-b", "editor", true, true)
	// Misses are conclusive denials while the grant set is complete.
	assertScopedRole("module-a", "admin", false, true)
	assertScopedRole("module-c", "analyst", false, true)

	if granted, conclusive := HasScopedRole(context.Background(), "module-a", "analyst"); granted || !conclusive {
		t.Errorf("empty context: got (%v,%v), want (false,true)", granted, conclusive)
	}
}

// TestHasScopedRoleInconclusiveWhenTruncated proves a miss under truncation is
// NOT a denial — the caller must fall back to CheckPermission.
func TestHasScopedRoleInconclusiveWhenTruncated(t *testing.T) {
	ctx := withScopedRolesTruncated(
		withScopedRoles(context.Background(), map[string][]string{"module-a": {"analyst"}}),
		true,
	)

	// A present grant is still conclusive.
	if granted, conclusive := HasScopedRole(ctx, "module-a", "analyst"); !granted || !conclusive {
		t.Errorf("present grant: got (%v,%v), want (true,true)", granted, conclusive)
	}
	// A miss is inconclusive: the header is incomplete.
	if granted, conclusive := HasScopedRole(ctx, "module-z", "analyst"); granted || conclusive {
		t.Errorf("truncated miss: got (%v,%v), want (false,false)", granted, conclusive)
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

	if granted, conclusive := HasScopedRole(ctx, "module-a", "analyst"); !granted || !conclusive {
		t.Fatalf("expected conclusive scoped role from header, got (%v,%v) from %v",
			granted, conclusive, ScopedRolesFromContext(ctx))
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
