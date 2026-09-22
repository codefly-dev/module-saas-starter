package business

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"sync"
	"testing"

	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
)

func declarationDigest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// capturedLogs collects what a reader reports. wool routes a log made from a
// background context to the fallback processor, which is exactly what this
// reader produces: the declaration's health is a property of the process, not
// of the exchange that happened to notice it.
type capturedLogs struct {
	mu   sync.Mutex
	logs []*wool.Log
}

func (c *capturedLogs) Process(log *wool.Log) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, log)
}

func (c *capturedLogs) countAt(level wool.Loglevel) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, log := range c.logs {
		if log.Level == level {
			count++
		}
	}
	return count
}

func captureDeclarationLogs(t *testing.T) *capturedLogs {
	t.Helper()
	previousLevel := wool.GlobalLogLevel()
	wool.SetGlobalLogLevel(wool.TRACE)
	captured := &capturedLogs{}
	wool.SetFallbackLogger(captured)
	t.Cleanup(func() {
		wool.SetFallbackLogger(nil)
		wool.SetGlobalLogLevel(previousLevel)
	})
	return captured
}

// A malformed declaration is sticky: it stays malformed until an operator fixes
// it, while every registrant keeps retrying on its own beat. Reporting per
// exchange would emit one identical line per beat per registrant for as long as
// the misconfiguration lasts, burying the line that says what is wrong.
func TestDeclarationReaderReportsAStickyFailureOnce(t *testing.T) {
	captured := captureDeclarationLogs(t)
	declared := "solution-a:not-a-digest"
	reader := &declarationReader{read: func() string { return declared }}

	for range 50 {
		require.Nil(t, reader.declared())
	}

	require.Equal(t, 1, captured.countAt(wool.WARN))
}

// Recovery is worth exactly one line too, and it re-arms the report so the next
// failure is not silently swallowed by the dedup state.
func TestDeclarationReaderReportsRecoveryAndRearms(t *testing.T) {
	captured := captureDeclarationLogs(t)
	declared := "solution-a:not-a-digest"
	reader := &declarationReader{read: func() string { return declared }}

	for range 5 {
		reader.declared()
	}
	require.Equal(t, 1, captured.countAt(wool.WARN))

	declared = "solution-a:" + declarationDigest("solution-a-secret")
	for range 5 {
		require.Contains(t, reader.declared(), "solution-a")
	}
	require.Equal(t, 1, captured.countAt(wool.INFO))
	require.Equal(t, 1, captured.countAt(wool.WARN))

	declared = "solution-a:still-not-a-digest"
	for range 5 {
		require.Nil(t, reader.declared())
	}
	require.Equal(t, 2, captured.countAt(wool.WARN))
}

// A declaration that has not changed must not be re-parsed. The exchange that
// reads it is not rate-limited by the gateway, so paying an allocation and a
// regexp per declared identity on every call is work an insider holding the
// perimeter credential could drive. Identity of the returned map is the direct
// evidence: a re-parse would hand back a different one.
func TestDeclarationReaderParsesOnlyWhenTheValueChanges(t *testing.T) {
	captureDeclarationLogs(t)
	declared := "solution-a:" + declarationDigest("solution-a-secret")
	reads := 0
	reader := &declarationReader{read: func() string {
		reads++
		return declared
	}}

	first := reader.declared()
	for range 10 {
		require.Equal(t,
			reflect.ValueOf(first).Pointer(),
			reflect.ValueOf(reader.declared()).Pointer(),
			"an unchanged declaration must not be re-parsed")
	}
	// The raw value is still read every time — that is what makes an edit
	// visible without a restart, and it is the half that must not be cached.
	require.Equal(t, 11, reads)

	declared += ",solution-b:" + declarationDigest("solution-b-secret")
	changed := reader.declared()
	require.NotEqual(t, reflect.ValueOf(first).Pointer(), reflect.ValueOf(changed).Pointer())
	require.Contains(t, changed, "solution-b")
}

// The reader is consulted from every in-flight exchange, so its cache has to be
// safe under concurrency. Run with -race.
func TestDeclarationReaderIsSafeUnderConcurrentReads(t *testing.T) {
	captureDeclarationLogs(t)
	var mu sync.RWMutex
	declared := "solution-a:" + declarationDigest("solution-a-secret")
	reader := &declarationReader{read: func() string {
		mu.RLock()
		defer mu.RUnlock()
		return declared
	}}

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := range 50 {
				if worker == 0 && round%10 == 0 {
					mu.Lock()
					declared = "solution-a:" + declarationDigest("solution-a-secret") +
						",solution-b:" + declarationDigest("rotated")
					mu.Unlock()
				}
				reader.declared()
			}
		}()
	}
	wg.Wait()

	require.Contains(t, reader.declared(), "solution-a")
}
