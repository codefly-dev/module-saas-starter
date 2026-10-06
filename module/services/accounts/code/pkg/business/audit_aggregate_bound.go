package business

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AuditAggregateMaxBytes bounds what one aggregation of a store of record keeps
// while it reads: the buckets it has opened, the samples a percentile collects
// and the values a distinct count remembers, 32 MiB as the export is. A store
// reads in windows and frees each window before the next, so the window budget
// bounds what a window holds; this bounds what lives past the windows, which
// otherwise grows with the history an aggregation without a time range reads
// (a percentile keeps a float per event, a distinct count a string per value,
// a grouping a bucket per key) however few buckets the answer has. A store
// gives up as soon as the state it has gathered passes the bound.
const AuditAggregateMaxBytes = 32 << 20

// ErrAuditAggregateTooLarge is the refusal of an aggregation whose state passes
// AuditAggregateMaxBytes. Narrowing the read (a time range, an event type, an
// actor) or grouping by fewer dimensions is the way past it. No partial answer
// is ever returned in its place.
var ErrAuditAggregateTooLarge = errors.New("audit aggregation: more is kept than one read holds; narrow it by time range, actor or event type, or group by fewer dimensions")

// auditAggregateResult is what an aggregation of a store of record returns to
// its caller: the buckets, or, when the store failed, no bucket and its error,
// which for a refusal for size is reported as a request the caller can narrow,
// not a server fault.
func auditAggregateResult(buckets []AuditAggregateBucket, err error) ([]AuditAggregateBucket, error) {
	switch {
	case err == nil:
		return buckets, nil
	case errors.Is(err, ErrAuditAggregateTooLarge):
		return nil, status.Error(codes.ResourceExhausted, ErrAuditAggregateTooLarge.Error())
	default:
		return nil, err
	}
}
