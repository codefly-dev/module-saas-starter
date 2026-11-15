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
	ended, end := context.WithCancel(context.Background())
	end()
	for name, tc := range map[string]struct {
		ctx   context.Context
		err   error
		code  codes.Code
		level wool.Loglevel
	}{
		"request ended":        {ended, fmt.Errorf("%w: post for %s: %w", business.ErrExecutionUnbound, credential, context.Canceled), codes.Canceled, wool.INFO},
		"dependency cancelled": {context.Background(), fmt.Errorf("%w: post for %s: %w", business.ErrExecutionUnbound, credential, context.Canceled), codes.Unavailable, wool.WARN},
		"deadline passed":      {context.Background(), fmt.Errorf("%w: post for %s: %w", business.ErrExecutionUnbound, credential, context.DeadlineExceeded), codes.DeadlineExceeded, wool.WARN},
		"review unbound":       {context.Background(), fmt.Errorf("%w: post for %s: dial tcp 10.0.0.1:443", business.ErrExecutionUnbound, credential), codes.Unavailable, wool.WARN},
		"unknown failure":      {context.Background(), fmt.Errorf("the reviewer read %s and panicked", credential), codes.Internal, wool.WARN},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLeveledLogs(t)
			err := credentialRefusal(tc.ctx, tc.err, "the actor", credential)
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

// A credential shorter than the redaction floor is not replaced: a caller
// presenting "e" would otherwise rewrite every "e" in its own log line.
func TestAShortCredentialDoesNotGarbleTheLogLine(t *testing.T) {
	logs := captureLeveledLogs(t)
	err := credentialRefusal(context.Background(), fmt.Errorf("%w: the api server did not answer", business.ErrExecutionUnbound), "the actor", "e")
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Contains(t, logs.all(), "the api server did not answer", "the reason is logged as it was")
	require.NotContains(t, logs.all(), "[redacted workload credential]")
}

// A rejection whose caller has already ended its request is still a refusal,
// logged at WARN: a prober that cancels every call does not keep its forged
// credentials out of WARN. A rejection is answered, not logged by
// credentialRefusal; the business layer logs it (ModulePrefixOfWorkload), so
// here it must not be downgraded to a Canceled read failure.
func TestARejectionWithAnEndedContextIsRefused(t *testing.T) {
	ended, end := context.WithCancel(context.Background())
	end()
	logs := captureLeveledLogs(t)
	err := credentialRefusal(ended, fmt.Errorf("%w: forged", business.ErrExecutionTokenRejected), "the actor", "")
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Empty(t, logs.at(wool.INFO, "a workload credential could not be read"), "a verdict is never logged as a read the caller cut short")
}

// End to end on the workload() path: a forged credential presented by a
// caller that has already ended its request is refused, and logged at WARN.
func TestARejectionWithAnEndedContextIsLoggedAtWarn(t *testing.T) {
	newAdmissionFixture(t) // the host whose cluster never issued this credential
	ended, end := context.WithCancel(context.Background())
	end()
	logs := captureLeveledLogs(t)
	_, err := workload(ended, "a-forged-workload-credential", "the actor")
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Len(t, logs.at(wool.WARN, "workload credential refused"), 1, "%s", logs.all())
	require.Empty(t, logs.at(wool.INFO, "workload credential refused"))
}
