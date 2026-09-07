package infra

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

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
