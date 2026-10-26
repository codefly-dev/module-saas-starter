package business

import "context"

type verifiedWorkloadPresenterKey struct{}

// WithVerifiedWorkloadPresenter records identity authenticated by the trusted
// gateway adapter. Callers must verify gateway provenance before calling it;
// request fields and ordinary headers are not authenticated presenters.
func WithVerifiedWorkloadPresenter(ctx context.Context, principal string) context.Context {
	return context.WithValue(ctx, verifiedWorkloadPresenterKey{}, principal)
}

// VerifiedWorkloadPresenter returns only the adapter-authenticated context value.
func VerifiedWorkloadPresenter(ctx context.Context) string {
	principal, _ := ctx.Value(verifiedWorkloadPresenterKey{}).(string)
	return principal
}
