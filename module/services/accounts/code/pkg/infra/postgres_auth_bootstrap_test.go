package infra

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestAuthBootstrapRetryClassification(t *testing.T) {
	for _, code := range []string{"40001", "40P01"} {
		t.Run(code, func(t *testing.T) {
			err := fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: code})
			require.True(t, isRetryableAuthBootstrapError(err))
		})
	}

	for _, constraint := range []string{
		"idx_users_primary_email_lower",
		"user_identities_provider_unique",
	} {
		t.Run(constraint, func(t *testing.T) {
			err := fmt.Errorf("wrapped: %w", &pgconn.PgError{
				Code:           "23505",
				ConstraintName: constraint,
			})
			require.True(t, isRetryableAuthBootstrapError(err))
		})
	}

	require.False(t, isRetryableAuthBootstrapError(
		&pgconn.PgError{Code: "23505"},
	))
	require.False(t, isRetryableAuthBootstrapError(
		&pgconn.PgError{Code: "23505", ConstraintName: "organizations_slug_key"},
	))
	require.False(t, isRetryableAuthBootstrapError(errors.New("application failure")))
}

func TestAuthBootstrapRetryWaitHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, waitForAuthBootstrapRetry(ctx, 1), context.Canceled)
}
