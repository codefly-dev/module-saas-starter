package auth

import (
	"context"
	"errors"
	"testing"
)

func TestVerifiedDatabaseIdentityFailsClosedAndCanonicalizesUUIDs(t *testing.T) {
	ctx := WithVerifiedDatabaseIdentity(
		context.Background(),
		"019f6bf7-5b1c-730d-9687-fe6d4aff31ed",
		"019f6bf7-5b4b-74e5-8c17-092259bb1661",
	)
	tenantID, userID, ok := VerifiedDatabaseIdentity(ctx)
	if !ok || tenantID != "019f6bf7-5b4b-74e5-8c17-092259bb1661" || userID != "019f6bf7-5b1c-730d-9687-fe6d4aff31ed" {
		t.Fatalf("verified identity = %q/%q ok=%t", tenantID, userID, ok)
	}

	invalid := WithVerifiedDatabaseIdentity(context.Background(), "caller-controlled", "org")
	if _, _, ok := VerifiedDatabaseIdentity(invalid); ok {
		t.Fatal("malformed identity was accepted as database authority")
	}

	if err := RequireVerifiedDatabaseScope(ctx,
		"019F6BF7-5B4B-74E5-8C17-092259BB1661",
		"019F6BF7-5B1C-730D-9687-FE6D4AFF31ED",
	); err != nil {
		t.Fatalf("canonical equivalent scope rejected: %v", err)
	}
	if err := RequireVerifiedDatabaseScope(context.Background(),
		"019f6bf7-5b4b-74e5-8c17-092259bb1661",
		"019f6bf7-5b1c-730d-9687-fe6d4aff31ed",
	); !errors.Is(err, ErrVerifiedDatabaseIdentityRequired) {
		t.Fatalf("missing verified identity error = %v", err)
	}
	if err := RequireVerifiedDatabaseScope(ctx,
		"019f6bf7-5b4b-74e5-8c17-092259bb1662",
		"019f6bf7-5b1c-730d-9687-fe6d4aff31ed",
	); !errors.Is(err, ErrVerifiedDatabaseScopeMismatch) {
		t.Fatalf("cross-tenant scope error = %v", err)
	}
}
