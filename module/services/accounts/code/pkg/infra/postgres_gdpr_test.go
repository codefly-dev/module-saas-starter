package infra_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// ============================================================================
// GDPR tests — gdpr_requests is RLS-protected (user-scoped); each test runs its
// store ops as the owning user via As(Identity{UserID}). NotFound runs as System
// (no owner to scope to).
// ============================================================================

func TestCreateAndGetGDPRRequest(t *testing.T) {
	userID := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		req := &business.GDPRRequest{
			ID:     business.NewIDString(),
			UserID: userID,
			Type:   business.GDPRExport,
			Status: business.GDPRPending,
			// A request that can still progress must name the job that owns it.
			JobID: business.NewIDString(),
		}
		require.NoError(t, testStore.CreateGDPRRequest(ctx, req))

		got, err := testStore.GetGDPRRequest(ctx, req.ID)
		require.NoError(t, err)
		require.Equal(t, req.ID, got.ID)
		require.Equal(t, req.JobID, got.JobID)
		require.Equal(t, userID, got.UserID)
		require.Equal(t, business.GDPRExport, got.Type)
		require.Equal(t, business.GDPRPending, got.Status)
		require.Empty(t, got.DownloadURL)
		require.Nil(t, got.ExpiresAt)
		require.Empty(t, got.Error)
		require.Nil(t, got.CompletedAt)
		return nil
	}))
}

func TestGetGDPRRequest_NotFound(t *testing.T) {
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		_, err := testStore.GetGDPRRequest(ctx, business.NewIDString())
		require.Error(t, err)
		var storeErr *business.StoreError
		require.ErrorAs(t, err, &storeErr)
		require.Equal(t, business.ErrTypeNotFound, storeErr.StoreErrorType)
		return nil
	}))
}

// A privacy request only moves while the worker holding its current lease says
// so: an attempt whose lease was taken over cannot record a receipt or finalize
// the request the newer attempt now owns.
func TestGDPRRequestTransitionsRequireTheCurrentLease(t *testing.T) {
	userID := seedUser(t)
	req := &business.GDPRRequest{
		ID:     business.NewIDString(),
		UserID: userID,
		Type:   business.GDPRExport,
		Status: business.GDPRPending,
		JobID:  business.NewIDString(),
	}
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		return testStore.CreateGDPRRequest(ctx, req)
	}))

	jobID := req.JobID
	first := business.GDPRLease{
		JobID: jobID, Owner: "worker-a", Token: business.NewIDString(), Attempt: 1,
	}
	second := business.GDPRLease{
		JobID: jobID, Owner: "worker-b", Token: business.NewIDString(), Attempt: 2,
	}

	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		claimed, err := testStore.ClaimGDPRRequest(ctx, req.ID, first)
		require.NoError(t, err)
		require.Equal(t, business.GDPRProcessing, claimed.Status)
		require.EqualValues(t, 1, claimed.Attempt)
		require.NoError(t, testStore.RecordGDPRStepReceipt(ctx, req.ID, first, "collect", "receipt-1"))

		// The lease expires and a second worker takes the request over.
		claimed, err = testStore.ClaimGDPRRequest(ctx, req.ID, second)
		require.NoError(t, err)
		require.Equal(t, map[string]string{"collect": "receipt-1"}, claimed.StepReceipts,
			"receipts survive a lease handover so the new attempt can skip finished steps")

		_, err = testStore.ClaimGDPRRequest(ctx, req.ID, first)
		require.ErrorIs(t, err, business.ErrPrivacyLeaseLost,
			"a stale attempt cannot take the request back from the one that replaced it")
		require.ErrorIs(t,
			testStore.RecordGDPRStepReceipt(ctx, req.ID, first, "publish", "receipt-2"),
			business.ErrPrivacyLeaseLost)
		require.ErrorIs(t,
			testStore.FinishGDPRRequest(ctx, req.ID, first, business.GDPROutcome{
				Status: business.GDPRCompleted, DownloadURL: "https://storage.example.com/stale.zip",
			}),
			business.ErrPrivacyLeaseLost)

		expiresAt := time.Now().Add(time.Hour)
		require.NoError(t, testStore.FinishGDPRRequest(ctx, req.ID, second, business.GDPROutcome{
			Status:      business.GDPRCompleted,
			DownloadURL: "https://storage.example.com/export.zip",
			ExpiresAt:   &expiresAt,
		}))

		got, err := testStore.GetGDPRRequest(ctx, req.ID)
		require.NoError(t, err)
		require.Equal(t, business.GDPRCompleted, got.Status)
		require.Equal(t, "https://storage.example.com/export.zip", got.DownloadURL)
		require.NotNil(t, got.CompletedAt)
		require.Empty(t, got.LeaseToken)

		// A redelivered job for completed work is returned untouched.
		claimed, err = testStore.ClaimGDPRRequest(ctx, req.ID, second)
		require.NoError(t, err)
		require.Equal(t, business.GDPRCompleted, claimed.Status)
		return nil
	}))
}

func TestExpiredGDPRExportArtifactsAreListedAndCleared(t *testing.T) {
	userID := seedUser(t)
	lease := business.GDPRLease{
		Owner: "worker-a", Token: business.NewIDString(), Attempt: 1,
	}
	lapsed := &business.GDPRRequest{
		ID: business.NewIDString(), UserID: userID, Type: business.GDPRExport,
		Status: business.GDPRPending, JobID: business.NewIDString(),
	}
	live := &business.GDPRRequest{
		ID: business.NewIDString(), UserID: userID, Type: business.GDPRExport,
		Status: business.GDPRPending, JobID: business.NewIDString(),
	}
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateGDPRRequest(ctx, lapsed))
		return testStore.CreateGDPRRequest(ctx, live)
	}))

	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		past := time.Now().Add(-time.Hour)
		future := time.Now().Add(time.Hour)
		for _, seed := range []struct {
			request   *business.GDPRRequest
			expiresAt time.Time
		}{{lapsed, past}, {live, future}} {
			seeded := lease
			seeded.JobID = seed.request.JobID
			_, err := testStore.ClaimGDPRRequest(ctx, seed.request.ID, seeded)
			require.NoError(t, err)
			expiresAt := seed.expiresAt
			require.NoError(t, testStore.FinishGDPRRequest(ctx, seed.request.ID, seeded, business.GDPROutcome{
				Status:      business.GDPRCompleted,
				DownloadURL: "https://storage.example.com/" + seed.request.ID + ".zip",
				ExpiresAt:   &expiresAt,
			}))
		}

		expired, err := testStore.ListExpiredGDPRExports(ctx, time.Now(), 10)
		require.NoError(t, err)
		require.Len(t, expired, 1)
		require.Equal(t, lapsed.ID, expired[0].ID)

		require.NoError(t, testStore.ClearGDPRExportArtifacts(ctx, []string{lapsed.ID}))
		expired, err = testStore.ListExpiredGDPRExports(ctx, time.Now(), 10)
		require.NoError(t, err)
		require.Empty(t, expired, "a cleared artifact is not swept again")

		got, err := testStore.GetGDPRRequest(ctx, lapsed.ID)
		require.NoError(t, err)
		require.Empty(t, got.DownloadURL)
		require.Equal(t, business.GDPRCompleted, got.Status)
		return nil
	}))
}

func TestGetUserGDPRRequests(t *testing.T) {
	userID := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		for _, rtype := range []business.GDPRRequestType{business.GDPRExport, business.GDPRDeletion} {
			req := &business.GDPRRequest{
				ID:     business.NewIDString(),
				UserID: userID,
				Type:   rtype,
				Status: business.GDPRPending,
				JobID:  business.NewIDString(),
			}
			require.NoError(t, testStore.CreateGDPRRequest(ctx, req))
		}

		requests, err := testStore.GetUserGDPRRequests(ctx, userID)
		require.NoError(t, err)
		require.Len(t, requests, 2)
		for _, r := range requests {
			require.Equal(t, userID, r.UserID)
		}
		return nil
	}))
}

func TestGetUserGDPRRequests_Empty(t *testing.T) {
	userID := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		requests, err := testStore.GetUserGDPRRequests(ctx, userID)
		require.NoError(t, err)
		require.Empty(t, requests)
		return nil
	}))
}

// An operator replaying a dead-lettered request produces a new job whose
// attempts restart at one, so the attempt number cannot fence it. What fences
// it is whether any attempt currently holds the row: every transition that ends
// an attempt clears the lease, so a replay may take over a released request and
// may not touch one still being worked.
//
// The claim deliberately does not consult the worker's copy of its job lease
// expiry: that copy is captured once and never refreshed while the heartbeat
// keeps extending the real lease, so testing against it would refuse attempts
// whose lease is alive.
func TestReplayedPrivacyJobClaimsOnlyAReleasedRequest(t *testing.T) {
	userID := seedUser(t)
	req := &business.GDPRRequest{
		ID:     business.NewIDString(),
		UserID: userID,
		Type:   business.GDPRExport,
		Status: business.GDPRPending,
		JobID:  business.NewIDString(),
	}
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		return testStore.CreateGDPRRequest(ctx, req)
	}))

	working := business.GDPRLease{
		JobID: req.JobID, Owner: "worker-a", Token: business.NewIDString(), Attempt: 3,
	}
	replay := business.GDPRLease{
		JobID: business.NewIDString(), Owner: "worker-b", Token: business.NewIDString(), Attempt: 1,
	}

	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		_, err := testStore.ClaimGDPRRequest(ctx, req.ID, working)
		require.NoError(t, err)

		_, err = testStore.ClaimGDPRRequest(ctx, req.ID, replay)
		require.ErrorIs(t, err, business.ErrPrivacyLeaseLost,
			"a replayed job cannot take a request another attempt is still working")

		// The working attempt ends, releasing the lease.
		require.NoError(t, testStore.FinishGDPRRequest(ctx, req.ID, working, business.GDPROutcome{
			Status: business.GDPRFailed, FailureCode: "privacy.provider_unavailable", Error: "down",
		}))

		claimed, err := testStore.ClaimGDPRRequest(ctx, req.ID, replay)
		require.NoError(t, err, "a released request is claimable by the replayed job")
		require.Equal(t, business.GDPRProcessing, claimed.Status)
		require.EqualValues(t, 1, claimed.Attempt,
			"the replayed job restarts attempts without the claim refusing it")
		return nil
	}))
}
