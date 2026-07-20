package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

var (
	// ErrVerifiedDatabaseIdentityRequired means the request never crossed a
	// trusted authentication boundary. Transport headers and wool values are
	// deliberately insufficient database or authorization-cache authority.
	ErrVerifiedDatabaseIdentityRequired = errors.New("verified database identity is required")
	// ErrVerifiedDatabaseScopeMismatch means a domain artifact names a tenant
	// or user other than the principal bound by the authentication interceptor.
	ErrVerifiedDatabaseScopeMismatch = errors.New("artifact scope does not match verified database identity")
)

// verifiedDatabaseIdentityKey is deliberately private. Database scope cannot
// be created by caller-controlled headers or by setting wool context values;
// only the authentication interceptors that successfully verify a token or a
// trusted gateway assertion can install it.
type verifiedDatabaseIdentityKey struct{}

type verifiedDatabaseIdentity struct {
	tenantID string
	userID   string
}

// WithVerifiedDatabaseIdentity binds canonical tenant/user UUIDs to ctx for
// the service-postgres authenticator. Invalid or zero UUIDs leave the context
// unauthenticated, so malformed trusted-forwarding data fails closed.
func WithVerifiedDatabaseIdentity(ctx context.Context, userID, tenantID string) context.Context {
	user, userErr := uuid.Parse(userID)
	tenant, tenantErr := uuid.Parse(tenantID)
	if ctx == nil || userErr != nil || tenantErr != nil || user == uuid.Nil || tenant == uuid.Nil {
		return ctx
	}
	return context.WithValue(ctx, verifiedDatabaseIdentityKey{}, verifiedDatabaseIdentity{
		tenantID: tenant.String(),
		userID:   user.String(),
	})
}

// VerifiedDatabaseIdentity returns only identity installed through
// WithVerifiedDatabaseIdentity; transport metadata alone is never consulted.
func VerifiedDatabaseIdentity(ctx context.Context) (tenantID, userID string, ok bool) {
	if ctx == nil {
		return "", "", false
	}
	identity, ok := ctx.Value(verifiedDatabaseIdentityKey{}).(verifiedDatabaseIdentity)
	if !ok || identity.tenantID == "" || identity.userID == "" {
		return "", "", false
	}
	return identity.tenantID, identity.userID, true
}

// RequireVerifiedDatabaseScope validates an immutable tenant/user artifact
// before any database-independent authorization optimization (notably Redis)
// is consulted. service-postgres repeats this check at the database boundary;
// keeping it here as well guarantees that a cache hit cannot bypass verified
// request scope.
func RequireVerifiedDatabaseScope(ctx context.Context, tenantID, userID string) error {
	verifiedTenantID, verifiedUserID, ok := VerifiedDatabaseIdentity(ctx)
	if !ok {
		return ErrVerifiedDatabaseIdentityRequired
	}
	tenant, tenantErr := uuid.Parse(tenantID)
	user, userErr := uuid.Parse(userID)
	if tenantErr != nil || userErr != nil || tenant == uuid.Nil || user == uuid.Nil {
		return ErrVerifiedDatabaseScopeMismatch
	}
	if tenant.String() != verifiedTenantID || user.String() != verifiedUserID {
		return ErrVerifiedDatabaseScopeMismatch
	}
	return nil
}
