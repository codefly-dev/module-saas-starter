package auth

import (
	"context"
	"errors"

	"github.com/codefly-dev/core/wool"

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
	// Propagate the same pair onto wool's identity dimension, so everything
	// downstream — logs, traces, the rate-limit key, an organization-defaulted
	// read — sees whose request this is without being handed it.
	//
	// Set as context values rather than through wool.Wool.WithOrgID, which
	// cannot work: `with` assigns to the Wool's OWN ctx field, and wool.Get
	// returns a fresh Wool per call, so the derived context is discarded and no
	// later wool.Get(ctx).OrgID() can see it. The keys are exported and
	// wool.Wool.lookup reads them off the context, so this is the propagation
	// the accessors were written for.
	//
	// It is bound HERE and nowhere else for the same reason the verified
	// identity is: this is the one door, it has already parsed both UUIDs, and
	// the invalid path returned above — so wool's organization can only ever
	// carry a verified value. That matters now that it is not merely an
	// observability field: a key service selects an organization's envelope key
	// by it.
	ctx = context.WithValue(ctx, wool.OrgIDKey, tenant.String())
	ctx = context.WithValue(ctx, wool.UserIDKey, user.String())
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
