package infra

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	eventsv1 "accounts/pkg/gen/saas/events/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestEventFingerprintIgnoresPerAttemptMetadata is the idempotency-retry guard:
// two publishes of one logical event carry the same fact but legitimately differ
// in the per-attempt transport/trace metadata (event_time, traceparent,
// correlation/causation ids). The fingerprint must be identical so a retry is
// treated as the idempotent re-publish it is, not a spurious conflict.
func TestEventFingerprintIgnoresPerAttemptMetadata(t *testing.T) {
	base := func() *eventsv1.EventEnvelope {
		return &eventsv1.EventEnvelope{
			Id:          "11111111-1111-1111-1111-111111111111",
			Type:        "scope.granted",
			Source:      "saas.accounts",
			Subject:     "scope/abc",
			Specversion: "1.0",
			Data:        []byte(`{"scope":"abc"}`),
			TenantId:    "22222222-2222-2222-2222-222222222222",
			BoundaryId:  "root",
		}
	}
	first := base()
	first.Time = timestamppb.New(time.Unix(1000, 0))
	first.Traceparent = "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	first.CorrelationId = "corr-1"
	first.CausationId = "cause-1"

	second := base()
	second.Time = timestamppb.New(time.Unix(9999, 0))
	second.Traceparent = "00-cccccccccccccccccccccccccccccccc-dddddddddddddddd-01"
	second.CorrelationId = "corr-2"
	second.CausationId = "cause-2"

	fpFirst, err := eventFingerprint(first)
	require.NoError(t, err)
	fpSecond, err := eventFingerprint(second)
	require.NoError(t, err)
	require.Equal(t, fpFirst, fpSecond,
		"fingerprint must ignore per-attempt event_time/traceparent/correlation/causation metadata")
}

// TestEventFingerprintTracksSemanticFact guards the other direction: a change to
// the event's actual fact (its payload here) must change the fingerprint, so a
// genuine same-id redefinition is still caught as a conflict.
func TestEventFingerprintTracksSemanticFact(t *testing.T) {
	base := &eventsv1.EventEnvelope{
		Id:          "11111111-1111-1111-1111-111111111111",
		Type:        "scope.granted",
		Source:      "saas.accounts",
		Specversion: "1.0",
		Data:        []byte(`{"scope":"abc"}`),
		TenantId:    "22222222-2222-2222-2222-222222222222",
	}
	changed := &eventsv1.EventEnvelope{
		Id:          "11111111-1111-1111-1111-111111111111",
		Type:        "scope.granted",
		Source:      "saas.accounts",
		Specversion: "1.0",
		Data:        []byte(`{"scope":"xyz"}`),
		TenantId:    "22222222-2222-2222-2222-222222222222",
	}
	fpBase, err := eventFingerprint(base)
	require.NoError(t, err)
	fpChanged, err := eventFingerprint(changed)
	require.NoError(t, err)
	require.NotEqual(t, fpBase, fpChanged,
		"a change to the event's data must change the fingerprint")
}

func TestNackMessageTruncatesOnRuneBoundary(t *testing.T) {
	// A cause whose byte length exceeds the failure-message cap and whose final
	// rune straddles the 4096th byte would, on a naive byte slice, become
	// invalid UTF-8 and make the nack command fail its protobuf validation.
	cause := errors.New(strings.Repeat("a", 4095) + "é")
	message := nackMessage(cause)
	require.LessOrEqual(t, len(message), 4096)
	require.True(t, utf8.ValidString(message), "a truncated nack message must remain valid UTF-8")
	require.Equal(t, strings.Repeat("a", 4095), message)
}
