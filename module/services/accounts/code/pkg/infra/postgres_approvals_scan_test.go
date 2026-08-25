package infra

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeRow feeds scanApprovalRequest column values without a database. It assigns
// vals[i] into the i-th Scan destination (indices matching approvalRequestColumns
// order); a nil entry leaves that destination at its zero value.
type fakeRow struct{ vals []any }

func (r fakeRow) Scan(dest ...any) error {
	for i, d := range dest {
		if i < len(r.vals) && r.vals[i] != nil {
			reflect.ValueOf(d).Elem().Set(reflect.ValueOf(r.vals[i]))
		}
	}
	return nil
}

// A policy JSONB that is valid JSON but does not fit ApprovalPolicy (approver_set
// is a list of strings) must fail the scan closed: silently defaulting would drop
// the approver-set restriction and the self-approval block on a corrupt row.
func TestScanApprovalRequest_MalformedPolicyFailsClosed(t *testing.T) {
	vals := make([]any, 16)
	vals[7] = []byte(`{"approver_set": 123}`) // policy column

	_, err := scanApprovalRequest(fakeRow{vals: vals})
	require.Error(t, err)
}

func TestScanApprovalRequest_WellFormedPolicyScans(t *testing.T) {
	vals := make([]any, 16)
	vals[7] = []byte(`{"approver_set": ["approver-1"], "allow_self": true}`)

	r, err := scanApprovalRequest(fakeRow{vals: vals})
	require.NoError(t, err)
	require.Equal(t, []string{"approver-1"}, r.Policy.ApproverSet)
	require.True(t, r.Policy.AllowSelf)
}
