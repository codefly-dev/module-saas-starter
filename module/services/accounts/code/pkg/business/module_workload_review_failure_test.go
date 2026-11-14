//go:build pure

package business

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
)

// A workload credential the review RAN and refused is a verdict on the
// caller. A review that could not run (the request ended, the deadline
// passed, the API server did not answer) decided nothing about the caller,
// so it is never returned as a rejection: read as one, a cancelled request
// was refused as a credential that proves no module.
func TestAWorkloadReviewThatCouldNotRunIsNotARejection(t *testing.T) {
	for name, tc := range map[string]struct {
		reviewErr error
		rejected  bool
		cause     error
	}{
		"request ended":   {reviewErr: fmt.Errorf("post tokenreviews: %w", context.Canceled), cause: context.Canceled},
		"deadline passed": {reviewErr: fmt.Errorf("post tokenreviews: %w", context.DeadlineExceeded), cause: context.DeadlineExceeded},
		"api unreachable": {reviewErr: errors.New("dial tcp: connection refused")},
		"review refused":  {reviewErr: fmt.Errorf("%w: the token has expired", ErrExecutionTokenRejected), rejected: true},
	} {
		t.Run(name, func(t *testing.T) {
			svc := reviewService()
			svc.executionReviewer = &fakeExecutionReviewer{reviewErr: tc.reviewErr}

			_, prefixErr := svc.ModulePrefixOfWorkload(context.Background(), "a-workload-credential")
			edgeErr := svc.RequireModuleEdge(context.Background(), "a-workload-credential")
			if tc.rejected {
				require.ErrorIs(t, prefixErr, ErrExecutionTokenRejected)
				require.NotErrorIs(t, prefixErr, ErrExecutionUnbound)
				require.ErrorIs(t, edgeErr, ErrNotTheModuleEdge, "a refused credential is not the module edge")
				return
			}
			require.ErrorIs(t, prefixErr, ErrExecutionUnbound)
			require.NotErrorIs(t, prefixErr, ErrExecutionTokenRejected)
			require.ErrorIs(t, edgeErr, ErrExecutionUnbound)
			require.NotErrorIs(t, edgeErr, ErrNotTheModuleEdge, "a review that could not run decided nothing about the caller")
			if tc.cause != nil {
				require.ErrorIs(t, prefixErr, tc.cause)
				require.ErrorIs(t, edgeErr, tc.cause)
			}
		})
	}
}

// A caller that ended its own request is no operational signal: the refusal is
// still logged, below WARN. Every other refusal stays a WARN.
func TestAWorkloadReviewTheCallerEndedIsLoggedBelowWarn(t *testing.T) {
	for name, tc := range map[string]struct {
		reviewErr error
		warn      bool
	}{
		"request ended":   {fmt.Errorf("post tokenreviews: %w", context.Canceled), false},
		"api unreachable": {errors.New("dial tcp: connection refused"), true},
		"review refused":  {fmt.Errorf("%w: the token has expired", ErrExecutionTokenRejected), true},
	} {
		t.Run(name, func(t *testing.T) {
			var levels []wool.Loglevel
			capture := &levelCapture{levels: &levels}
			wool.SetFallbackLogger(capture)
			t.Cleanup(func() { wool.SetFallbackLogger(nil) })
			svc := reviewService()
			svc.executionReviewer = &fakeExecutionReviewer{reviewErr: tc.reviewErr}
			_, err := svc.ModulePrefixOfWorkload(context.Background(), "a-workload-credential")
			require.Error(t, err)
			require.Len(t, levels, 1)
			if tc.warn {
				require.Equal(t, wool.WARN, levels[0])
			} else {
				require.Less(t, levels[0], wool.WARN)
			}
		})
	}
}

type levelCapture struct{ levels *[]wool.Loglevel }

func (c *levelCapture) Process(msg *wool.Log) { *c.levels = append(*c.levels, msg.Level) }
