package jobs_test

import (
	"errors"
	"testing"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/stretchr/testify/require"
)

func TestStateMachineAllowsOnlyCanonicalTransitions(t *testing.T) {
	states := []jobsv1.JobState{
		jobsv1.JobState_JOB_STATE_UNSPECIFIED,
		jobsv1.JobState_JOB_STATE_PENDING,
		jobsv1.JobState_JOB_STATE_PROCESSING,
		jobsv1.JobState_JOB_STATE_RETRYING,
		jobsv1.JobState_JOB_STATE_SUCCEEDED,
		jobsv1.JobState_JOB_STATE_DEAD_LETTER,
		jobsv1.JobState_JOB_STATE_CANCELED,
		jobsv1.JobState(99),
	}
	allowed := map[[2]jobsv1.JobState]bool{
		{jobsv1.JobState_JOB_STATE_PENDING, jobsv1.JobState_JOB_STATE_PROCESSING}:     true,
		{jobsv1.JobState_JOB_STATE_PENDING, jobsv1.JobState_JOB_STATE_CANCELED}:       true,
		{jobsv1.JobState_JOB_STATE_PROCESSING, jobsv1.JobState_JOB_STATE_RETRYING}:    true,
		{jobsv1.JobState_JOB_STATE_PROCESSING, jobsv1.JobState_JOB_STATE_SUCCEEDED}:   true,
		{jobsv1.JobState_JOB_STATE_PROCESSING, jobsv1.JobState_JOB_STATE_DEAD_LETTER}: true,
		{jobsv1.JobState_JOB_STATE_RETRYING, jobsv1.JobState_JOB_STATE_PROCESSING}:    true,
		{jobsv1.JobState_JOB_STATE_RETRYING, jobsv1.JobState_JOB_STATE_CANCELED}:      true,
	}

	for _, from := range states {
		for _, to := range states {
			want := allowed[[2]jobsv1.JobState{from, to}]
			require.Equal(t, want, jobs.CanTransition(from, to), "%s -> %s", from, to)
			err := jobs.ValidateTransition(from, to)
			if want {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, jobs.ErrInvalidTransition)
			}
		}
	}
}

func TestInitialAndTerminalStates(t *testing.T) {
	require.NoError(t, jobs.ValidateInitialState(jobsv1.JobState_JOB_STATE_PENDING))
	require.ErrorIs(t, jobs.ValidateInitialState(jobsv1.JobState_JOB_STATE_PROCESSING), jobs.ErrInvalidInitialState)

	require.False(t, jobs.IsTerminal(jobsv1.JobState_JOB_STATE_PENDING))
	require.False(t, jobs.IsTerminal(jobsv1.JobState_JOB_STATE_PROCESSING))
	require.False(t, jobs.IsTerminal(jobsv1.JobState_JOB_STATE_RETRYING))
	require.True(t, jobs.IsTerminal(jobsv1.JobState_JOB_STATE_SUCCEEDED))
	require.True(t, jobs.IsTerminal(jobsv1.JobState_JOB_STATE_DEAD_LETTER))
	require.True(t, jobs.IsTerminal(jobsv1.JobState_JOB_STATE_CANCELED))
}

func TestDatabaseStateRoundTrip(t *testing.T) {
	states := []jobsv1.JobState{
		jobsv1.JobState_JOB_STATE_PENDING,
		jobsv1.JobState_JOB_STATE_PROCESSING,
		jobsv1.JobState_JOB_STATE_RETRYING,
		jobsv1.JobState_JOB_STATE_SUCCEEDED,
		jobsv1.JobState_JOB_STATE_DEAD_LETTER,
		jobsv1.JobState_JOB_STATE_CANCELED,
	}
	for _, state := range states {
		stored, err := jobs.DatabaseState(state)
		require.NoError(t, err)
		parsed, err := jobs.ParseDatabaseState(stored)
		require.NoError(t, err)
		require.Equal(t, state, parsed)
	}

	_, err := jobs.DatabaseState(jobsv1.JobState_JOB_STATE_UNSPECIFIED)
	require.True(t, errors.Is(err, jobs.ErrUnknownState))
	_, err = jobs.ParseDatabaseState("delivered")
	require.True(t, errors.Is(err, jobs.ErrUnknownState))
}

func TestDatabaseDirectionRoundTrip(t *testing.T) {
	for _, direction := range []jobsv1.JobDirection{
		jobsv1.JobDirection_JOB_DIRECTION_INBOX,
		jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
	} {
		database, err := jobs.DatabaseDirection(direction)
		require.NoError(t, err)
		parsed, err := jobs.ParseDatabaseDirection(database)
		require.NoError(t, err)
		require.Equal(t, direction, parsed)
	}

	_, err := jobs.DatabaseDirection(jobsv1.JobDirection_JOB_DIRECTION_UNSPECIFIED)
	require.ErrorIs(t, err, jobs.ErrUnknownDirection)
	_, err = jobs.ParseDatabaseDirection("sideways")
	require.ErrorIs(t, err, jobs.ErrUnknownDirection)
}
