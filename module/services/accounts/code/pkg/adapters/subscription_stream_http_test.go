package adapters

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"accounts/pkg/auth"
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
	payloads   map[string]business.JournalPayload
	// opened closes when a reader has resolved its starting cursor. A stream
	// starting live begins at whatever the journal held then, so a test that
	// appends before that point is testing a different stream than it means to.
	opened     chan struct{}
	openedOnce sync.Once
}

func (s *streamStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *streamStore) ListTenantJournal(_ context.Context, _ string, afterSeq int64, limit int) ([]business.JournalEntry, error) {
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

func (s *streamStore) ResolveTenantJournalCursor(_ context.Context, _, eventID string) (int64, bool, error) {
	defer s.openedOnce.Do(func() { close(s.opened) })
	s.mu.Lock()
	defer s.mu.Unlock()
	var head int64
	for _, entry := range s.journal {
		if eventID != "" && entry.EventID == eventID {
			return entry.Seq, true, nil
		}
		if entry.Seq > head {
			head = entry.Seq
		}
	}
	return head, false, nil
}

func (s *streamStore) LoadTenantJournalPayloads(
	_ context.Context, _ string, eventIDs []string, maxBytes int,
) (map[string]business.JournalPayload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]business.JournalPayload{}
	for _, id := range eventIDs {
		payload, ok := s.payloads[id]
		if !ok || len(payload.Data) > maxBytes {
			continue
		}
		out[id] = payload
	}
	return out, nil
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

func (s *streamStore) setPayload(eventID, contentType string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payloads[eventID] = business.JournalPayload{ContentType: contentType, Data: data}
}

func (s *streamStore) append(entry business.JournalEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.journal = append(s.journal, entry)
}

func newStreamStore(accessibleIDs ...string) *streamStore {
	store := &streamStore{
		accessible: map[string]bool{},
		payloads:   map[string]business.JournalPayload{},
		opened:     make(chan struct{}),
	}
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
	store.setPayload("01930000-0000-7000-8000-000000000002",
		"application/json", []byte("{\n\"version\": 7\n}"))
	store.append(business.JournalEntry{
		Seq: 2, EventID: "01930000-0000-7000-8000-000000000002", Type: streamedChangeType,
		Subject: "entry-1", EventTime: time.Unix(2, 0).UTC(),
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
	// The entry the cursor names is offered again: the window below the cursor is
	// what recovers a transaction that committed after the cursor passed its seq,
	// and the reader cannot tell a late commit from one it already sent. Delivery
	// is at-least-once, as it is everywhere else in this contract, and a client
	// dedupes on the event id.
	require.Contains(t, body, "id: 01930000-0000-7000-8000-000000000001")
}

// A stream with no cursor starts at the head, not at the beginning of history.
// The look-back window means it reaches a bounded distance behind that head —
// bounded is the property, because an unbounded one would replay the tenant's
// whole journal to every new reader.
func TestSubscriptionStreamStartsLiveWithoutACursor(t *testing.T) {
	useGatewayToken(t)
	store := newStreamStore("entry-1")
	for seq := int64(1); seq <= 300; seq++ {
		store.append(business.JournalEntry{
			Seq: seq, EventID: fmt.Sprintf("01930000-0000-7000-8000-%012d", seq),
			Type: streamedChangeType, Subject: "entry-1",
		})
	}
	handler := newStreamHandler(t, store)

	req, cancel := streamRequest(t, nil)
	response, stop := serveUntil(t, handler, req, cancel)
	defer stop()
	awaitOpen(t, store)

	store.append(business.JournalEntry{
		Seq: 301, EventID: "01930000-0000-7000-8000-000000000301",
		Type: streamedChangeType, Subject: "entry-1",
	})
	body := awaitBody(t, response, "01930000-0000-7000-8000-000000000301")
	require.NotContains(t, body, "01930000-0000-7000-8000-000000000001",
		"a live stream must not replay the tenant's history")
	require.NotContains(t, body, "01930000-0000-7000-8000-000000000200",
		"the look-back is bounded to the re-read window, not the whole journal")
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

// Every poll re-reads a window below the cursor so a late-committing entry is
// recovered. That window is offered again each time, so the reader must send
// each entry once — otherwise a client sees the same change repeatedly for as
// long as it stays inside the window.
func TestSubscriptionStreamSendsAReReadEntryOnlyOnce(t *testing.T) {
	useGatewayToken(t)
	store := newStreamStore("entry-1")
	handler := newStreamHandler(t, store)

	req, cancel := streamRequest(t, nil)
	response, stop := serveUntil(t, handler, req, cancel)
	defer stop()
	awaitOpen(t, store)

	store.append(business.JournalEntry{
		Seq: 1, EventID: "01930000-0000-7000-8000-000000000001",
		Type: streamedChangeType, Subject: "entry-1",
	})
	awaitBody(t, response, "01930000-0000-7000-8000-000000000001")

	// Several more polls run over the same window before this returns.
	store.append(business.JournalEntry{
		Seq: 2, EventID: "01930000-0000-7000-8000-000000000002",
		Type: streamedChangeType, Subject: "entry-1",
	})
	body := awaitBody(t, response, "01930000-0000-7000-8000-000000000002")
	require.Equal(t, 1, strings.Count(body, "id: 01930000-0000-7000-8000-000000000001"),
		"an entry inside the re-read window must be sent once, not once per poll")
}

// A cursor the tenant cannot resolve — aged out of retention, or never theirs —
// starts the stream at the head. Saying nothing would let the client conclude it
// missed nothing, so it is told to re-sync. This leaks nothing: "not in your
// tenant" is what the caller already knows.
func TestSubscriptionStreamAnnouncesAnUnresolvedCursor(t *testing.T) {
	useGatewayToken(t)
	store := newStreamStore("entry-1")
	store.append(business.JournalEntry{
		Seq: 1, EventID: "01930000-0000-7000-8000-000000000001",
		Type: streamedChangeType, Subject: "entry-1",
	})
	handler := newStreamHandler(t, store)

	req, cancel := streamRequest(t, map[string]string{
		"Last-Event-ID": "01930000-0000-7000-8000-0000000000ff",
	})
	response, stop := serveUntil(t, handler, req, cancel)
	defer stop()

	body := awaitBody(t, response, "event: reset")
	require.Contains(t, body, "cursor not resolved")

	// A cursor that DOES resolve gets no reset frame.
	req2, cancel2 := streamRequest(t, map[string]string{
		"Last-Event-ID": "01930000-0000-7000-8000-000000000001",
	})
	store2 := newStreamStore("entry-1")
	store2.append(business.JournalEntry{
		Seq: 1, EventID: "01930000-0000-7000-8000-000000000001",
		Type: streamedChangeType, Subject: "entry-1",
	})
	response2, stop2 := serveUntil(t, newStreamHandler(t, store2), req2, cancel2)
	defer stop2()
	awaitOpen(t, store2)
	store2.append(business.JournalEntry{
		Seq: 2, EventID: "01930000-0000-7000-8000-000000000002",
		Type: streamedChangeType, Subject: "entry-1",
	})
	require.NotContains(t, awaitBody(t, response2, "000000000002"), "event: reset")
}

// A forwarded gateway identity carries no expiry this process can re-check, so
// the lifetime bound is the only thing stopping a stream outliving the
// credential that opened it. It must therefore be shorter than an access token's
// own life.
func TestSubscriptionStreamLifetimeIsShorterThanAnAccessToken(t *testing.T) {
	// ed25519.Config defaults AccessTokenTTL to three minutes; a stream that
	// outlived it would keep serving a logged-out person.
	require.Less(t, subscriptionStreamMaxLifetime, 3*time.Minute)
}

// revocableAccessMinter stops verifying its token on demand, the way a logout or
// an expiry does. It overrides VerifyAccess with its own lock because the stream
// re-verifies on a goroutine while the test revokes on another.
type revocableAccessMinter struct {
	fixedAccessMinter
	mu      sync.Mutex
	revoked bool
}

func (m *revocableAccessMinter) VerifyAccess(string) (*auth.Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.revoked {
		return nil, errors.New("access token is invalid")
	}
	return m.identity, nil
}

func (m *revocableAccessMinter) revoke() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoked = true
}

// A bearer is re-verified on every poll, so a token that stops verifying ends
// the stream rather than riding out the lifetime bound.
func TestSubscriptionStreamEndsWhenTheBearerStopsVerifying(t *testing.T) {
	useGatewayToken(t)
	store := newStreamStore("entry-1")
	service, err := business.NewService(store)
	require.NoError(t, err)
	service.SetFollowables([]business.FollowableResource{
		{ResourceType: streamedResourceType, Events: []string{streamedChangeType}},
	})

	minter := &revocableAccessMinter{fixedAccessMinter: fixedAccessMinter{identity: &auth.Identity{
		UserID: uuid.Must(uuid.NewV7()),
		OrgID:  uuid.Must(uuid.NewV7()),
	}}}
	service.SetJWTMinter(minter)
	handler := NewSubscriptionStreamHandler(service)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, SubscriptionStreamPath, nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer live-token")

	response := newSyncBody()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(response, req)
	}()
	awaitOpen(t, store)

	minter.revoke()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the stream outlived the credential that opened it")
	}
	require.Equal(t, http.StatusOK, response.Status())
}
