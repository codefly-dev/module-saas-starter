package adapters

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// leveledLogs records each line wool's fallback logger processes, with its level.
type leveledLogs struct {
	mu    sync.Mutex
	lines []*wool.Log
}

func (l *leveledLogs) Process(msg *wool.Log) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, msg)
}

// at returns the lines logged at level whose text contains substring.
func (l *leveledLogs) at(level wool.Loglevel, substring string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range l.lines {
		if line.Level == level && strings.Contains(line.String(), substring) {
			out = append(out, line.String())
		}
	}
	return out
}

func (l *leveledLogs) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range l.lines {
		out = append(out, line.String())
	}
	return strings.Join(out, "\n")
}

func captureLeveledLogs(t *testing.T) *leveledLogs {
	t.Helper()
	logs := &leveledLogs{}
	wool.SetFallbackLogger(logs)
	t.Cleanup(func() { wool.SetFallbackLogger(nil) })
	return logs
}

// A credential read failure is logged without the credential, as
// ModulePrefixOfWorkload logs its refusals, and at the level its cause
// deserves: a caller ending its own request is no operational signal, so it is
// logged below WARN; a deadline, a review that could not run, or anything else
// is a WARN.
func TestACredentialReadFailureIsLoggedRedactedAndByCause(t *testing.T) {
	const credential = "workload-credential-not-for-logs"
	for name, tc := range map[string]struct {
		err   error
		code  codes.Code
		level wool.Loglevel
	}{
		"request ended":   {fmt.Errorf("%w: post for %s: %w", business.ErrExecutionUnbound, credential, context.Canceled), codes.Canceled, wool.INFO},
		"deadline passed": {fmt.Errorf("%w: post for %s: %w", business.ErrExecutionUnbound, credential, context.DeadlineExceeded), codes.DeadlineExceeded, wool.WARN},
		"review unbound":  {fmt.Errorf("%w: post for %s: dial tcp 10.0.0.1:443", business.ErrExecutionUnbound, credential), codes.Unavailable, wool.WARN},
		"unknown failure": {fmt.Errorf("the reviewer read %s and panicked", credential), codes.Internal, wool.WARN},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLeveledLogs(t)
			err := credentialRefusal(context.Background(), tc.err, "the actor", credential)
			require.Equal(t, tc.code, status.Code(err), "%v", err)
			require.Len(t, logs.at(tc.level, "a workload credential could not be read"), 1, "logged once, at %v: %s", tc.level, logs.all())
			require.NotContains(t, logs.all(), credential, "the credential is never logged")
			require.Contains(t, logs.all(), "[redacted workload credential]")
		})
	}
}

// On the workload() path the business layer already logs the refusal
// (ModulePrefixOfWorkload); the adapter does not log it a second time.
func TestAWorkloadCredentialReadFailureIsLoggedOnce(t *testing.T) {
	f := newAdmissionFixture(t)
	f.cluster.Unreachable = true
	logs := captureLeveledLogs(t)
	_, err := workload(context.Background(), f.hop, "the actor")
	require.Equal(t, codes.Unavailable, status.Code(err), "%v", err)
	require.Len(t, logs.at(wool.WARN, "the api server did not answer"), 1, "one WARN for one failed read: %s", logs.all())
}
