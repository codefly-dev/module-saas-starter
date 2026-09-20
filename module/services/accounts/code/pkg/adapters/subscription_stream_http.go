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

	// subscriptionStreamMaxLifetime bounds one connection. A forwarded gateway
	// identity carries no expiry this process can re-check, so the only thing that
	// stops such a stream outliving the credential that opened it is this bound —
	// which therefore has to be SHORTER than an access token's own life (three
	// minutes, ed25519.Config.AccessTokenTTL), not longer. The client reconnects
	// with its last event id, and the re-read window means the bound costs no
	// entries. A bearer is additionally re-verified on every poll, which stops a
	// revoked session within the poll interval rather than at this bound.
	subscriptionStreamMaxLifetime = 2 * time.Minute
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
	// DataOmitted tells a client the payload exists but was not carried inline, so
	// it re-reads from the owning module instead of treating the entry as
	// payloadless.
	DataOmitted bool `json:"dataOmitted,omitempty"`
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

		presented := r.Header.Get("Last-Event-ID")
		cursor, resolvedCursor, err := svc.OpenSubscriptionCursor(ctx, orgID, presented)
		if err != nil {
			wool.Get(ctx).In("subscriptionStream").Error("cannot open the journal cursor", wool.ErrField(err))
			writeJSONError(w, http.StatusServiceUnavailable, "subscriptions are temporarily unavailable")
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store, no-transform")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		// A cursor that resolved to nothing is not the same as no cursor at all: the
		// client believes it is holding a position, and the stream is about to start
		// at the tenant's head instead. Retention aged it out, or it was never this
		// tenant's — indistinguishable here, and deliberately so, because telling
		// the two apart is the existence oracle the resolution avoids. Either way
		// the client is told, so it re-syncs rather than concluding it missed
		// nothing.
		if presented != "" && !resolvedCursor {
			if _, err := w.Write([]byte("event: reset\ndata: {\"reason\":\"cursor not resolved\"}\n\n")); err != nil {
				return
			}
			_ = http.NewResponseController(w).Flush()
		}

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

	// Every poll re-reads a window below the cursor so an entry whose producing
	// transaction committed late is still delivered. That window is offered again
	// on each poll, so the reader remembers what it has sent and drops the repeat;
	// an entry falling out of the window behind the cursor is forgotten with it,
	// which is what keeps this bounded rather than growing for the connection's
	// life.
	delivered := map[string]int64{}

	for {
		if r.Context().Err() != nil || time.Now().After(deadline) {
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

		events, next, more, err := svc.ReadSubscriptions(polled, orgID, userID, cursor)
		if err != nil {
			wool.Get(polled).In("subscriptionStream").Error("cannot read the journal", wool.ErrField(err))
			return
		}

		wrote := false
		for _, event := range events {
			if _, repeat := delivered[event.EventID]; repeat {
				continue
			}
			if err := writeStreamedEvent(w, event); err != nil {
				return
			}
			delivered[event.EventID] = event.Seq
			wrote = true
		}

		cursor = next
		forgetDeliveredBelow(delivered, cursor)

		switch {
		case wrote:
			lastFrame = time.Now()
		case time.Since(lastFrame) >= subscriptionStreamHeartbeat:
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
			lastFrame = time.Now()
		default:
			// Nothing written, so nothing to flush; go straight on rather than
			// paying for a flush that would do nothing.
			if more {
				continue
			}
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
			}
			continue
		}

		if err := control.Flush(); err != nil {
			return
		}
		// A page that did not exhaust what is waiting is drained immediately: a
		// client resuming after an outage would otherwise crawl forward one page
		// per tick no matter how far behind it is.
		if more {
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// forgetDeliveredBelow drops what the re-read window can no longer offer again.
// An entry below the window cannot come back, so remembering it would only grow
// the set for as long as the connection lives.
func forgetDeliveredBelow(delivered map[string]int64, cursor int64) {
	floor := cursor - business.JournalRescanDepth
	for id, seq := range delivered {
		if seq < floor {
			delete(delivered, id)
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
	frame.DataOmitted = event.DataOmitted
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
