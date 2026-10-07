package infra

import (
	"accounts/pkg/business"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHistoryPayloadDecodeStopsAtDecodedContainerBudget(t *testing.T) {
	raw := []byte(`{"items":[` + strings.Repeat(`{},`, 100) + `{}` + `]}`)
	_, err := decodeBoundedHistoryPayload(raw, int64(len(raw))+256)
	require.ErrorIs(t, err, business.ErrAuditHistoryWindowTooLarge)
}

func TestHistoryPayloadBoundedDecodePreservesCanonicalNumbers(t *testing.T) {
	raw := []byte(`{"large":9007199254740993,"nested":[{"value":0.0012300},null,true,"text"]}`)
	expected, err := decodeQueuedPayload(raw)
	require.NoError(t, err)
	actual, err := decodeBoundedHistoryPayload(raw, 10000)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
}
