package clickhousestore

import (
	"errors"
	"fmt"

	"accounts/pkg/business"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"
)

// rowDefectCodes are the server exception codes that mean the content of a row
// is malformed for its column, so that a row refused alone with one of them is
// refused every time. Each name is as ClickHouse's errorCodeToName gives it.
//
// Parsing: a value the server cannot read as the column's type.
//
//	6   CANNOT_PARSE_TEXT                    26  CANNOT_PARSE_QUOTED_STRING
//	27  CANNOT_PARSE_INPUT_ASSERTION_FAILED  38  CANNOT_PARSE_DATE
//	41  CANNOT_PARSE_DATETIME                72  CANNOT_PARSE_NUMBER
//	117 INCORRECT_DATA
//
// Type and range: a value that is not, or cannot become, one the column holds.
//
//	53  TYPE_MISMATCH                        70  CANNOT_CONVERT_TYPE
//	321 VALUE_IS_OUT_OF_RANGE_OF_DATA_TYPE
//
// Size: a value over what the column holds.
//
//	131 TOO_LARGE_STRING_SIZE
//
// Constraint: a row that a CHECK constraint the deployment put on the table
// refuses.
//
//	469 VIOLATED_CONSTRAINT
//
// Every other code is retryable, and never makes a row refused, whatever the
// number of times it repeats: network and socket errors (210, 209), timeouts
// (159), memory (241), a read-only server or table (164, 242), no space (243),
// too many parts or queries (252, 202), the quorum and replica codes (285, 286,
// 319), Keeper (999), a missing table (60), access and authentication (497, 516),
// and the limits on a whole block (158, 396), which are the block's, not a row's.
// A code that is not known here is retryable too: the cost of a wrong guess in
// that direction is a queue that waits, never an event set aside.
var rowDefectCodes = map[int32]string{
	6: "CANNOT_PARSE_TEXT", 26: "CANNOT_PARSE_QUOTED_STRING", 27: "CANNOT_PARSE_INPUT_ASSERTION_FAILED",
	38: "CANNOT_PARSE_DATE", 41: "CANNOT_PARSE_DATETIME", 72: "CANNOT_PARSE_NUMBER", 117: "INCORRECT_DATA",
	53: "TYPE_MISMATCH", 70: "CANNOT_CONVERT_TYPE", 321: "VALUE_IS_OUT_OF_RANGE_OF_DATA_TYPE",
	131: "TOO_LARGE_STRING_SIZE",
	469: "VIOLATED_CONSTRAINT",
}

// isRowDefect reports whether err is a server exception whose code says a row's
// content is malformed.
func isRowDefect(err error) bool {
	var exception *clickhouse.Exception
	if !errors.As(err, &exception) {
		return false
	}
	_, defect := rowDefectCodes[exception.Code]
	return defect
}

// isUnencodableRow reports whether err is the driver's refusal to encode a value
// of the row being appended as its column's type (clickhouse-go's AppendRow
// error). It is raised before anything is sent, and names the row because the
// store appends rows one at a time. A wrong number of values (the driver's
// Append error) is the store's own bug, not a row's content, and is not this.
func isUnencodableRow(err error) bool {
	var block *proto.BlockError
	return errors.As(err, &block) && block.Op == "AppendRow"
}

// refusal is the refusal of the row, whose first value is its event id.
func refusal(table string, row []any, cause error) error {
	return &business.PermanentRowRejection{
		EventID: fmt.Sprint(row[0]),
		Cause:   fmt.Errorf("clickhouse audit store: %s refused the row: %w", table, cause),
	}
}
