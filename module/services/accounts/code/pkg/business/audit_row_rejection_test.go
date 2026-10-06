package business

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPermanentRowRejectionsReadsWhatTheStoreAttributed(t *testing.T) {
	one := &PermanentRowRejection{EventID: "event-1", Cause: errors.New("invalid")}
	two := &PermanentRowRejection{EventID: "event-2", Cause: errors.New("too large")}
	retryable := errors.New("429")

	require.Nil(t, PermanentRowRejections(nil))
	require.Nil(t, PermanentRowRejections(retryable))
	require.Nil(t, PermanentRowRejections(&PermanentRowRejection{Cause: errors.New("no event")}), "a refusal that names no event names no row")

	require.Equal(t, []*PermanentRowRejection{one}, PermanentRowRejections(one))
	require.Equal(t, []*PermanentRowRejection{one}, PermanentRowRejections(fmt.Errorf("append: %w", one)))
	require.Equal(t, []*PermanentRowRejection{one, two}, PermanentRowRejections(errors.Join(one, retryable, fmt.Errorf("again: %w", two), one)),
		"several, beside a retryable cause, each once")
	require.ErrorContains(t, one, "event-1")
	require.ErrorContains(t, one, "invalid")
	require.ErrorIs(t, one, one.Cause)
}
