package executioncustody

import (
	base "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"testing"
)

func TestClaimsDigestV1Vector(t *testing.T) {
	// Wire hex: 72047461736b7a0773657373696f6e. JSON formatting is immaterial.
	const expected = "wc-claims-proto-v1:099cbc0b6ef3b1584d0f34fd61448593710c5d6a4a8baea831df9b5ec07e797b"
	for _, evidence := range []string{`{"taskId":"task","sessionId":"session"}`, `{ "session_id": "session", "task_id": "task" }`} {
		var claims base.WorkContextV1
		require.NoError(t, protojson.Unmarshal([]byte(evidence), &claims))
		actual, err := ClaimsDigest(&claims)
		require.NoError(t, err)
		require.Equal(t, expected, actual)
	}
	var unknown base.WorkContextV1
	unknown.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
	_, err := ClaimsDigest(&unknown)
	require.Error(t, err)
}
