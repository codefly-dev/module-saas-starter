package adapters

// GET /v1/subscriptions/stream — the signed-in person's live view of the
// committed journal, as Server-Sent Events.
//
// It does not fit the proto model: one request answers with an open-ended
// sequence of frames rather than a message, so it is mounted beside the
// grpc-gateway mux through RegisterHTTPRoute. Authentication is the same private
// identity every other transport establishes; raw X-User-Id / X-Org-Id headers
// are never authority.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"github.com/codefly-dev/core/wool"
)

// SubscriptionStreamPath is the route the stream is mounted at. RegisterHTTPRoute
// matches on a prefix, so the handler pins the exact path itself rather than
// answering for every sibling under it.
const SubscriptionStreamPath = "/v1/subscriptions/stream"

const (
	// subscriptionStreamPoll is how long the reader waits before looking for new
	// entries. The journal has no change signal of its own, so the stream reads
	// forward on a timer; the interval is what "within seconds" costs, one bounded
	// query per connected client.
	subscriptionStreamPoll = time.Second

	// subscriptionStreamHeartbeat keeps an idle connection alive through proxies
	// that close a silent one. A comment frame carries no event and never advances
	// the client's last event id.
	subscriptionStreamHeartbeat = 15 * time.Second

	// subscriptionStreamMaxLifetime bounds one connection. Authority is resolved
	// when the stream opens, and a forwarded gateway identity carries no expiry
	// this process can re-check, so the stream ends and the client reconnects —
	// with its last event id, so the bound costs no events. A bearer is re-verified
	// on every poll in addition to this, which is what makes a revoked session stop
	// receiving within the poll interval rather than at the end of the lifetime.
	subscriptionStreamMaxLifetime = 5 * time.Minute
)

// streamedEvent is what one frame carries. It names the entry and the change,
// and carries the producer's payload through unread: the host resolves what the
// entry is from the composed catalog and never decodes another module's data.
type streamedEvent struct {
	Type            string          `json:"type"`
	ResourceType    string          `json:"resourceType"`
	ResourceID      string          `json:"resourceId"`
	Time            string          `json:"time,omitempty"`
	DataContentType string          `json:"dataContentType,omitempty"`
	Data            json.RawMessage `json:"data,omitempty"`
}

func NewSubscriptionStreamHandler(svc *business.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != SubscriptionStreamPath {
			writeJSONError(w, http.StatusNotFound, "Route not found")
			return
		}
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		ctx, userID, orgID, err := authenticateHTTPRequest(svc, r)
		if err != nil {
			writeStreamAuthnError(w, r, err)
			return
		}

		cursor, err := svc.OpenSubscriptionCursor(ctx, orgID, r.Header.Get("Last-Event-ID"))
		if err != nil {
			wool.Get(ctx).In("subscriptionStream").Error("cannot open the journal cursor", wool.ErrField(err))
			writeJSONError(w, http.StatusServiceUnavailable, "subscriptions are temporarily unavailable")
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		streamSubscriptions(w, r, svc, userID, orgID, cursor)
	})
}

func streamSubscriptions(
	w http.ResponseWriter, r *http.Request,
	svc *business.Service, userID, orgID string, cursor int64,
) {
	control := http.NewResponseController(w)
	deadline := time.Now().Add(subscriptionStreamMaxLifetime)
	lastFrame := time.Now()
	ticker := time.NewTicker(subscriptionStreamPoll)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
		if time.Now().After(deadline) {
			return
		}
		// Re-establishing the identity on every poll is what keeps an expired or
		// revoked bearer from holding a stream open past the credential that opened
		// it. The subject cannot change under a live connection — a different
		// credential is a different request — so a re-verification that no longer
		// answers the same person ends the stream rather than switching to them.
		polled, polledUser, polledOrg, err := authenticateHTTPRequest(svc, r)
		if err != nil || polledUser != userID || polledOrg != orgID {
			return
		}

		events, next, err := svc.ReadSubscriptions(polled, orgID, userID, cursor)
		if err != nil {
			wool.Get(polled).In("subscriptionStream").Error("cannot read the journal", wool.ErrField(err))
			return
		}
		cursor = next

		for _, event := range events {
			if err := writeStreamedEvent(w, event); err != nil {
				return
			}
		}
		if len(events) == 0 {
			if time.Since(lastFrame) < subscriptionStreamHeartbeat {
				continue
			}
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
		}
		lastFrame = time.Now()
		if err := control.Flush(); err != nil {
			return
		}
	}
}

func writeStreamedEvent(w http.ResponseWriter, event business.SubscriptionEvent) error {
	frame := streamedEvent{
		Type:         event.Type,
		ResourceType: event.ResourceType,
		ResourceID:   event.ResourceID,
	}
	if !event.Time.IsZero() {
		frame.Time = event.Time.UTC().Format(time.RFC3339Nano)
	}
	// The payload is a producer's own bytes and has never been through this
	// process. It travels verbatim when it is declared and well-formed JSON —
	// marshalling re-encodes it compactly, so a newline inside it cannot split the
	// frame — and is dropped otherwise rather than guessed at. Either way the
	// entry, its change type and its versions' owner are named, so a client that
	// needs the content reads it from the module that owns it.
	if isJSONContentType(event.DataContentType) && json.Valid(event.Data) {
		frame.DataContentType = event.DataContentType
		frame.Data = json.RawMessage(event.Data)
	}
	body, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte("id: " + event.EventID + "\nevent: " + event.Type + "\ndata: " + string(body) + "\n\n"))
	return err
}

// isJSONContentType reports whether the producer declared a JSON payload.
// datacontenttype is a CloudEvents media type, so it may carry parameters.
func isJSONContentType(contentType string) bool {
	base, _, _ := strings.Cut(contentType, ";")
	switch strings.TrimSpace(base) {
	case "application/json", "text/json":
		return true
	default:
		return false
	}
}

func writeStreamAuthnError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrRevocationUnavailable) {
		wool.Get(r.Context()).In("subscriptionStream").Warn("revocation list unavailable, denying (fail-closed)", wool.ErrField(err))
		writeJSONError(w, http.StatusServiceUnavailable, "authorization temporarily unavailable")
		return
	}
	writeJSONError(w, http.StatusUnauthorized, "authentication required")
}
