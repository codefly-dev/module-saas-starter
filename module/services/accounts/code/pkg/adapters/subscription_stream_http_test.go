package adapters

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type streamStore struct {
	business.Store

	mu         sync.Mutex
	journal    []business.JournalEntry
	accessible map[string]bool
	// opened closes when a reader has resolved its starting cursor. A stream
	// starting live begins at whatever the journal held then, so a test that
	// appends before that point is testing a different stream than it means to.
	opened     chan struct{}
	openedOnce sync.Once
}

func (s *streamStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *streamStore) ListTenantJournal(_ context.Context, afterSeq int64, limit int) ([]business.JournalEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := make([]business.JournalEntry, 0, limit)
	for _, entry := range s.journal {
		if entry.Seq > afterSeq && len(page) < limit {
			page = append(page, entry)
		}
	}
	return page, nil
}

func (s *streamStore) ResolveTenantJournalCursor(_ context.Context, eventID string) (int64, error) {
	defer s.openedOnce.Do(func() { close(s.opened) })
	s.mu.Lock()
	defer s.mu.Unlock()
	var head int64
	for _, entry := range s.journal {
		if eventID != "" && entry.EventID == eventID {
			return entry.Seq, nil
		}
		if entry.Seq > head {
			head = entry.Seq
		}
	}
	return head, nil
}

func (s *streamStore) ListAccessibleResourceIDs(
	_ context.Context, _, _ string, _ gen.SubjectKind, resourceType, _ string, candidates []string,
) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, id := range candidates {
		if s.accessible[resourceType+"|"+id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *streamStore) append(entry business.JournalEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.journal = append(s.journal, entry)
}

func newStreamStore(accessibleIDs ...string) *streamStore {
	store := &streamStore{accessible: map[string]bool{}, opened: make(chan struct{})}
	for _, id := range accessibleIDs {
		store.accessible[streamedResourceType+"|"+id] = true
	}
	return store
}

// syncBody is the response the stream writes into. httptest.ResponseRecorder
// buffers without a lock, and here the handler writes on its own goroutine while
// the test reads — so the recorder itself would be the race, not the code.
type syncBody struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	header http.Header
	status int
}

func newSyncBody() *syncBody {
	return &syncBody{header: http.Header{}, status: http.StatusOK}
}

func (b *syncBody) Header() http.Header { return b.header }

func (b *syncBody) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBody) WriteHeader(status int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status = status
}

func (b *syncBody) Flush() {}

func (b *syncBody) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBody) Status() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status
}

const (
	streamedResourceType = "documents.entry"
	streamedChangeType   = "documents.entry.version_minted"
)

func newStreamHandler(t *testing.T, store *streamStore) http.Handler {
	t.Helper()
	service, err := business.NewService(store)
	require.NoError(t, err)
	service.SetFollowables([]business.FollowableResource{
		{ResourceType: streamedResourceType, Events: []string{streamedChangeType}},
	})
	return NewSubscriptionStreamHandler(service)
}

// streamRequest builds a gateway-forwarded request: the identity headers are
// authority only beside the gateway credential, which the caller installs.
func streamRequest(t *testing.T, headers map[string]string) (*http.Request, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, SubscriptionStreamPath, nil).WithContext(ctx)
	req.Header.Set("X-Codefly-Gateway-Token", "test-gateway-token")
	req.Header.Set("X-User-Id", uuid.NewString())
	req.Header.Set("X-Org-Id", uuid.NewString())
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	return req, cancel
}

func useGatewayToken(t *testing.T) {
	t.Helper()
	previous := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previous) })
}

// serveUntil runs the handler in the background and returns the recorder plus a
// stop function; the stream only ends when the request context is cancelled.
func serveUntil(t *testing.T, handler http.Handler, req *http.Request, cancel context.CancelFunc) (*syncBody, func()) {
	t.Helper()
	response := newSyncBody()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(response, req)
	}()
	return response, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the stream did not close after its request was cancelled")
		}
	}
}

// awaitBody polls the response until it contains want.
func awaitBody(t *testing.T, response *syncBody, want string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		body := response.String()
		if strings.Contains(body, want) {
			return body
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("stream never carried %q; body was %q", want, response.String())
	return ""
}

// awaitOpen blocks until the stream has resolved its starting cursor.
func awaitOpen(t *testing.T, store *streamStore) {
	t.Helper()
	select {
	case <-store.opened:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream never resolved its starting cursor")
	}
}

func TestSubscriptionStreamDeliversAVisibleChangeAndHidesTheRest(t *testing.T) {
	useGatewayToken(t)
	store := newStreamStore("entry-1")
	handler := newStreamHandler(t, store)

	req, cancel := streamRequest(t, nil)
	response, stop := serveUntil(t, handler, req, cancel)
	defer stop()
	awaitOpen(t, store)

	store.append(business.JournalEntry{
		Seq: 1, EventID: "01930000-0000-7000-8000-000000000001", Type: streamedChangeType,
		Subject: "hidden-entry", EventTime: time.Unix(1, 0).UTC(),
	})
	store.append(business.JournalEntry{
		Seq: 2, EventID: "01930000-0000-7000-8000-000000000002", Type: streamedChangeType,
		Subject: "entry-1", EventTime: time.Unix(2, 0).UTC(),
		DataContentType: "application/json", Data: []byte("{\n\"version\": 7\n}"),
	})

	body := awaitBody(t, response, "entry-1")
	require.Equal(t, "text/event-stream", response.Header().Get("Content-Type"))
	require.Equal(t, "no-store, no-transform", response.Header().Get("Cache-Control"))
	require.Equal(t, "no", response.Header().Get("X-Accel-Buffering"))
	require.Contains(t, body, "id: 01930000-0000-7000-8000-000000000002")
	require.Contains(t, body, "event: "+streamedChangeType)
	// The producer's payload travels verbatim but re-encoded, so a newline inside
	// it cannot split the frame into two.
	require.Contains(t, body, `"data":{"version":7}`)
	require.NotContains(t, body, "hidden-entry")
	require.NotContains(t, body, "01930000-0000-7000-8000-000000000001")
}

func TestSubscriptionStreamResumesFromTheLastEventID(t *testing.T) {
	useGatewayToken(t)
	store := newStreamStore("entry-1")
	for seq := int64(1); seq <= 3; seq++ {
		store.append(business.JournalEntry{
			Seq: seq, EventID: "01930000-0000-7000-8000-00000000000" + string(rune('0'+seq)),
			Type: streamedChangeType, Subject: "entry-1",
		})
	}
	handler := newStreamHandler(t, store)

	req, cancel := streamRequest(t, map[string]string{
		"Last-Event-ID": "01930000-0000-7000-8000-000000000001",
	})
	response, stop := serveUntil(t, handler, req, cancel)
	defer stop()

	body := awaitBody(t, response, "01930000-0000-7000-8000-000000000003")
	require.Contains(t, body, "01930000-0000-7000-8000-000000000002")
	require.NotContains(t, body, "id: 01930000-0000-7000-8000-000000000001")
}

func TestSubscriptionStreamStartsLiveWithoutACursor(t *testing.T) {
	useGatewayToken(t)
	store := newStreamStore("entry-1")
	store.append(business.JournalEntry{
		Seq: 1, EventID: "01930000-0000-7000-8000-000000000001",
		Type: streamedChangeType, Subject: "entry-1",
	})
	handler := newStreamHandler(t, store)

	req, cancel := streamRequest(t, nil)
	response, stop := serveUntil(t, handler, req, cancel)
	defer stop()
	awaitOpen(t, store)

	store.append(business.JournalEntry{
		Seq: 2, EventID: "01930000-0000-7000-8000-000000000002",
		Type: streamedChangeType, Subject: "entry-1",
	})
	body := awaitBody(t, response, "01930000-0000-7000-8000-000000000002")
	require.NotContains(t, body, "01930000-0000-7000-8000-000000000001")
}

func TestSubscriptionStreamRefusesAnUnauthenticatedOrMisroutedRequest(t *testing.T) {
	useGatewayToken(t)
	handler := newStreamHandler(t, newStreamStore())

	for name, probe := range map[string]struct {
		method  string
		path    string
		headers map[string]string
		want    int
	}{
		"no credential":       {http.MethodGet, SubscriptionStreamPath, nil, http.StatusUnauthorized},
		"forged identity":     {http.MethodGet, SubscriptionStreamPath, map[string]string{"X-User-Id": uuid.NewString(), "X-Org-Id": uuid.NewString()}, http.StatusUnauthorized},
		"wrong method":        {http.MethodPost, SubscriptionStreamPath, nil, http.StatusMethodNotAllowed},
		"sibling of the path": {http.MethodGet, SubscriptionStreamPath + "/anything", nil, http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(probe.method, probe.path, nil)
			for header, value := range probe.headers {
				req.Header.Set(header, value)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			require.Equal(t, probe.want, recorder.Code, recorder.Body.String())
		})
	}
}
