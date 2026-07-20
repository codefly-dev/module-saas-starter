// Package jobs defines the reusable durable inbox/outbox lifecycle. The
// protobuf enum is the canonical vocabulary; this package owns behavior that
// cannot be expressed by an enum alone.
package jobs

import (
	"errors"
	"fmt"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

var (
	ErrInvalidInitialState = errors.New("jobs: new job must be pending")
	ErrInvalidTransition   = errors.New("jobs: invalid state transition")
	ErrUnknownDirection    = errors.New("jobs: unknown direction")
	ErrUnknownState        = errors.New("jobs: unknown state")
)

// DatabaseDirection maps the protobuf direction onto its PostgreSQL value.
func DatabaseDirection(direction jobsv1.JobDirection) (string, error) {
	switch direction {
	case jobsv1.JobDirection_JOB_DIRECTION_INBOX:
		return "inbox", nil
	case jobsv1.JobDirection_JOB_DIRECTION_OUTBOX:
		return "outbox", nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownDirection, direction)
	}
}

// ParseDatabaseDirection is the inverse of DatabaseDirection.
func ParseDatabaseDirection(direction string) (jobsv1.JobDirection, error) {
	switch direction {
	case "inbox":
		return jobsv1.JobDirection_JOB_DIRECTION_INBOX, nil
	case "outbox":
		return jobsv1.JobDirection_JOB_DIRECTION_OUTBOX, nil
	default:
		return jobsv1.JobDirection_JOB_DIRECTION_UNSPECIFIED,
			fmt.Errorf("%w: %q", ErrUnknownDirection, direction)
	}
}

// ValidateInitialState rejects persisted work that bypasses the single entry
// point into the state machine.
func ValidateInitialState(state jobsv1.JobState) error {
	if state != jobsv1.JobState_JOB_STATE_PENDING {
		return fmt.Errorf("%w: %s", ErrInvalidInitialState, state)
	}
	return nil
}

// CanTransition reports whether one durable state change is legal. Retrying is
// a scheduled, unleased state. Terminal jobs are immutable; replay creates a
// new pending job linked through replay_of.
func CanTransition(from, to jobsv1.JobState) bool {
	switch from {
	case jobsv1.JobState_JOB_STATE_PENDING:
		return to == jobsv1.JobState_JOB_STATE_PROCESSING ||
			to == jobsv1.JobState_JOB_STATE_CANCELED
	case jobsv1.JobState_JOB_STATE_PROCESSING:
		return to == jobsv1.JobState_JOB_STATE_RETRYING ||
			to == jobsv1.JobState_JOB_STATE_SUCCEEDED ||
			to == jobsv1.JobState_JOB_STATE_DEAD_LETTER
	case jobsv1.JobState_JOB_STATE_RETRYING:
		return to == jobsv1.JobState_JOB_STATE_PROCESSING ||
			to == jobsv1.JobState_JOB_STATE_CANCELED
	default:
		return false
	}
}

// ValidateTransition returns a stable sentinel-wrapped error for an invalid
// transition so workers can distinguish programmer errors from store failures.
func ValidateTransition(from, to jobsv1.JobState) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	return nil
}

// IsTerminal reports whether a job can never change state again.
func IsTerminal(state jobsv1.JobState) bool {
	return state == jobsv1.JobState_JOB_STATE_SUCCEEDED ||
		state == jobsv1.JobState_JOB_STATE_DEAD_LETTER ||
		state == jobsv1.JobState_JOB_STATE_CANCELED
}

// DatabaseState maps the protobuf source-of-truth enum onto the compact values
// used by PostgreSQL constraints and indexes.
func DatabaseState(state jobsv1.JobState) (string, error) {
	switch state {
	case jobsv1.JobState_JOB_STATE_PENDING:
		return "pending", nil
	case jobsv1.JobState_JOB_STATE_PROCESSING:
		return "processing", nil
	case jobsv1.JobState_JOB_STATE_RETRYING:
		return "retrying", nil
	case jobsv1.JobState_JOB_STATE_SUCCEEDED:
		return "succeeded", nil
	case jobsv1.JobState_JOB_STATE_DEAD_LETTER:
		return "dead_letter", nil
	case jobsv1.JobState_JOB_STATE_CANCELED:
		return "canceled", nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownState, state)
	}
}

// ParseDatabaseState is the inverse of DatabaseState.
func ParseDatabaseState(state string) (jobsv1.JobState, error) {
	switch state {
	case "pending":
		return jobsv1.JobState_JOB_STATE_PENDING, nil
	case "processing":
		return jobsv1.JobState_JOB_STATE_PROCESSING, nil
	case "retrying":
		return jobsv1.JobState_JOB_STATE_RETRYING, nil
	case "succeeded":
		return jobsv1.JobState_JOB_STATE_SUCCEEDED, nil
	case "dead_letter":
		return jobsv1.JobState_JOB_STATE_DEAD_LETTER, nil
	case "canceled":
		return jobsv1.JobState_JOB_STATE_CANCELED, nil
	default:
		return jobsv1.JobState_JOB_STATE_UNSPECIFIED, fmt.Errorf("%w: %q", ErrUnknownState, state)
	}
}
