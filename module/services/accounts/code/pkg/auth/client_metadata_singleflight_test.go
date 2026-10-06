package auth_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
)

// blockingFetcher counts outbound fetches and holds each one until released, so
// a test can have several callers in flight at once.
type blockingFetcher struct {
	calls    atomic.Int32
	release  chan struct{}
	document string
}

func (f *blockingFetcher) Fetch(_ context.Context, _ string) ([]byte, time.Duration, error) {
	f.calls.Add(1)
	<-f.release
	return []byte(f.document), time.Minute, nil
}

// r1/f8. Concurrent resolutions of one client_id make ONE outbound request.
//
// The cache alone cannot do this: it is populated only after a fetch returns, so
// a burst for an uncached document is a burst of outbound requests — this host
// reflecting inbound concurrency at a third-party origin one-for-one, each held
// for up to the fetch timeout.
func TestConcurrentResolutionsOfOneDocumentFetchOnce(t *testing.T) {
	const clientID = "https://client.example.com/metadata"
	fetcher := &blockingFetcher{
		release: make(chan struct{}),
		document: `{"client_id":"https://client.example.com/metadata",` +
			`"client_name":"Example",` +
			`"redirect_uris":["http://127.0.0.1/callback"],` +
			`"token_endpoint_auth_method":"none"}`,
	}
	policy, err := auth.NewClientMetadataPolicy("any")
	require.NoError(t, err)
	resolver := auth.NewClientMetadataResolverWith(policy, fetcher)

	const callers = 8
	var wg sync.WaitGroup
	results := make([]auth.RegisteredClient, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = resolver.Resolve(context.Background(), clientID)
		}()
	}
	// Let every caller reach the resolver before the fetch can complete, so they
	// are genuinely concurrent rather than serialised by a fast return.
	require.Eventually(t, func() bool { return fetcher.calls.Load() >= 1 },
		2*time.Second, time.Millisecond)
	close(fetcher.release)
	wg.Wait()

	require.Equal(t, int32(1), fetcher.calls.Load(),
		"eight concurrent callers must produce one outbound fetch")
	for i := range callers {
		require.NoError(t, errs[i])
		require.Equal(t, clientID, results[i].ClientID, "every caller gets the resolved client")
	}

	// And the fetch is not repeated once it is cached.
	_, err = resolver.Resolve(context.Background(), clientID)
	require.NoError(t, err)
	require.Equal(t, int32(1), fetcher.calls.Load())
}

// A caller whose own request is cancelled while waiting on another caller's
// fetch gives up: a shared fetch must not outlive the caller waiting on it.
func TestAWaitingResolverGivesUpWhenItsOwnRequestIsCancelled(t *testing.T) {
	const clientID = "https://client.example.com/metadata"
	fetcher := &blockingFetcher{release: make(chan struct{}), document: "{}"}
	policy, err := auth.NewClientMetadataPolicy("any")
	require.NoError(t, err)
	resolver := auth.NewClientMetadataResolverWith(policy, fetcher)

	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = resolver.Resolve(context.Background(), clientID)
	}()
	<-started
	require.Eventually(t, func() bool { return fetcher.calls.Load() == 1 },
		2*time.Second, time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = resolver.Resolve(ctx, clientID)
	require.ErrorIs(t, err, auth.ErrClientMetadataUnreachable)

	close(fetcher.release)
}
