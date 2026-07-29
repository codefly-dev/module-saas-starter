package infra_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

func insertWaitlistEntry(t *testing.T, state string) *business.WaitlistEntry {
	t.Helper()
	entry := &business.WaitlistEntry{
		ID:                    business.NewIDString(),
		Email:                 business.NewIDString() + "@example.com",
		State:                 state,
		ReferralCode:          business.NewIDString(),
		ConsentPolicyVersion:  business.CurrentConsentPolicyVersion,
		VerificationTokenHash: business.NewIDString(),
		VerificationExpiresAt: timePointer(time.Now().Add(time.Hour)),
	}
	var inserted *business.WaitlistEntry
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		result, err := testStore.UpsertWaitlistEntry(ctx, entry, time.Minute)
		if result != nil {
			inserted = result.Entry
		}
		return err
	}))
	require.NotNil(t, inserted)
	return inserted
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func TestConvertWaitlistEntryReturnsOnlyTheFirstTransition(t *testing.T) {
	entry := insertWaitlistEntry(t, "verified")
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	var first, replay *business.WaitlistEntry
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		var err error
		first, err = testStore.ConvertWaitlistEntry(
			ctx,
			entry.Email,
			userID,
			orgID,
			time.Now(),
		)
		return err
	}))
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		var err error
		replay, err = testStore.ConvertWaitlistEntry(
			ctx,
			entry.Email,
			userID,
			orgID,
			time.Now(),
		)
		return err
	}))

	require.NotNil(t, first)
	require.Equal(t, "converted", first.State)
	require.Nil(t, replay)
}

func TestInviteWaitlistEntryTransitionsOnlyApprovedLeads(t *testing.T) {
	entry := insertWaitlistEntry(t, "pending")

	var invited *business.WaitlistEntry
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		var err error
		invited, err = testStore.InviteWaitlistEntry(ctx, entry.ID, time.Now())
		return err
	}))
	require.Nil(t, invited)

	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		_, err := testStore.UpdateWaitlistState(
			ctx,
			entry.ID,
			"approved",
			"",
			nil,
			time.Now(),
		)
		return err
	}))
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		var err error
		invited, err = testStore.InviteWaitlistEntry(ctx, entry.ID, time.Now())
		return err
	}))
	require.NotNil(t, invited)
	require.Equal(t, "invited", invited.State)
}

func TestConcurrentWaitlistJoinsCreateOneEntry(t *testing.T) {
	emailAddress := business.NewIDString() + "@example.com"
	start := make(chan struct{})
	results := make(chan *business.WaitlistUpsertResult, 2)
	errors := make(chan error, 2)
	var workers sync.WaitGroup

	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			entry := &business.WaitlistEntry{
				ID:                    business.NewIDString(),
				Email:                 emailAddress,
				State:                 "pending",
				ReferralCode:          business.NewIDString(),
				ConsentPolicyVersion:  business.CurrentConsentPolicyVersion,
				VerificationTokenHash: business.NewIDString(),
				VerificationExpiresAt: timePointer(time.Now().Add(time.Hour)),
			}
			var result *business.WaitlistUpsertResult
			err := testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
				var upsertErr error
				result, upsertErr = testStore.UpsertWaitlistEntry(ctx, entry, time.Minute)
				return upsertErr
			})
			results <- result
			errors <- err
		}()
	}

	close(start)
	workers.Wait()
	close(results)
	close(errors)

	for err := range errors {
		require.NoError(t, err)
	}
	created := 0
	for result := range results {
		require.NotNil(t, result)
		require.NotNil(t, result.Entry)
		if result.Created {
			created++
		}
	}
	require.Equal(t, 1, created)
}

func TestConcurrentWaitlistVerificationReportsOneTransition(t *testing.T) {
	entry := insertWaitlistEntry(t, "pending")
	start := make(chan struct{})
	results := make(chan *business.WaitlistVerificationResult, 2)
	errors := make(chan error, 2)
	var workers sync.WaitGroup

	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			var result *business.WaitlistVerificationResult
			err := testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
				var verifyErr error
				result, verifyErr = testStore.VerifyWaitlistEntry(
					ctx,
					entry.VerificationTokenHash,
					time.Now(),
				)
				return verifyErr
			})
			results <- result
			errors <- err
		}()
	}

	close(start)
	workers.Wait()
	close(results)
	close(errors)

	for err := range errors {
		require.NoError(t, err)
	}
	transitioned := 0
	for result := range results {
		require.NotNil(t, result)
		require.NotNil(t, result.Entry)
		require.Equal(t, "verified", result.Entry.State)
		if result.Transitioned {
			transitioned++
		}
	}
	require.Equal(t, 1, transitioned)
}
