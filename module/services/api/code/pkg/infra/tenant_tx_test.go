package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"api/pkg/infra"
)

// TestWithOrgTx_RejectsEmptyOrgID — fail loud rather than silently
// matching an "empty" tenant under RLS. A bug elsewhere that
// forgets to thread orgID through must surface here, not pass and
// then return zero rows in production.
//
// Runs without a DB: the guard fires before any pool operation, so
// a nil-pool store is enough to prove the contract.
func TestWithOrgTx_RejectsEmptyOrgID(t *testing.T) {
	s := &infra.PostgresStore{}
	err := s.WithOrgTx(context.Background(), "", func(_ context.Context) error {
		return nil
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "orgID is required")
}

// Integration-style tests for WithOrgTx — verifying that
// app.current_org_id is set inside the tx and rolled back on error
// — live in pkg/business/service_test.go (which already has a
// real DB via WithDependencies). They'll be added when the first
// per-tenant table gets RLS enabled (see RLS_PLAN.md), so the
// test exists alongside its first real-world consumer.
