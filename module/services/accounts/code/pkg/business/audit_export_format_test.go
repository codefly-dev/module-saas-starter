package business

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// An unsupported export format is the caller's mistake and is refused as one,
// naming the formats that are supported. It used to surface as an internal
// error whose message the transport masks.
func TestExportAuditLogRefusesAnUnknownFormatByName(t *testing.T) {
	_, _, _, err := (&Service{}).ExportAuditLog(context.Background(), "", "EXPORT_FORMAT_JSON", "", "", nil)
	var storeErr *StoreError
	require.True(t, errors.As(err, &storeErr))
	require.Equal(t, ErrTypeValidation, storeErr.StoreErrorType)
	require.Contains(t, err.Error(), `use "csv" or "json"`)
}
