package business

import (
	"fmt"
)

// PermanentRowRejection is the error a store of record returns for a row it
// refused for the row's own content and will refuse every time it is sent: a
// value outside a column's range, a row over the size a request carries, text
// the store cannot hold. It names the event so the relay can set that one row
// aside and deliver the rest.
//
// It is the only evidence the relay accepts that a row is undeliverable. A
// store returns it only when the store itself attributed the refusal to the
// row; a transport error, a server error, a throttle, a quota, a timeout, a
// replica or quorum failure, and any error the store cannot classify are not
// this error, and the relay retries them with backoff and sets nothing aside.
type PermanentRowRejection struct {
	// EventID is the event whose row was refused.
	EventID string
	// Cause is what the store reported about the row.
	Cause error
	// Reason is what was wrong, in words that are the same for every row the store
	// refuses for the same cause and that carry nothing of one row: no event id, no
	// position in the request, no value. It is how the relay tells one cause that
	// refuses every row of a write — a table changed under the relay — from rows
	// refused one by one for their own content (RefusedForOneReason). Empty when
	// the store cannot state one, and a rejection without one is always judged as
	// a row.
	Reason string
}

func (e *PermanentRowRejection) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("audit store refused the row of event %s for good", e.EventID)
	}
	return fmt.Sprintf("audit store refused the row of event %s for good: %v", e.EventID, e.Cause)
}

// Unwrap returns what the store reported.
func (e *PermanentRowRejection) Unwrap() error { return e.Cause }

// PermanentRowRejections returns every row err says the store refused for good,
// one per event id, in the order err names them. err may be one rejection,
// wrapped, or several joined with errors.Join, with or without a retryable
// cause beside them. It returns nil when err names none — which is the case for
// every error that is not a refusal of a row.
func PermanentRowRejections(err error) []*PermanentRowRejection {
	var found []*PermanentRowRejection
	seen := map[string]bool{}
	var walk func(error)
	walk = func(err error) {
		switch e := err.(type) {
		case nil:
		case *PermanentRowRejection:
			if e != nil && e.EventID != "" && !seen[e.EventID] {
				seen[e.EventID] = true
				found = append(found, e)
			}
		case interface{ Unwrap() []error }:
			for _, inner := range e.Unwrap() {
				walk(inner)
			}
		case interface{ Unwrap() error }:
			walk(e.Unwrap())
		}
	}
	walk(err)
	return found
}

// RefusedForOneReason reports whether err refuses every one of eventIDs, and for
// one and the same Reason, which it returns.
//
// One cause refusing every row of a write says something about the store, not
// about the rows: a column added, dropped or retyped outside the kit refuses
// every row alike, while a row that is really at fault is refused beside rows
// that are not. So it is the whole write, refused alike, that the relay does not
// take as a refusal of each row (appendRows), and a store checks the same of one
// insert before it names rows at all.
//
// It needs two distinct events. A single row refused alone cannot be told from a
// store that refuses everything, and treating it as the store's condition would
// leave a poison row at the head of the queue for good whenever it is the only
// one a write carries, a batch size of one included. A rejection that states no
// Reason is never alike to another.
func RefusedForOneReason(err error, eventIDs []string) (reason string, whole bool) {
	reasons := make(map[string]string)
	for _, rejection := range PermanentRowRejections(err) {
		reasons[rejection.EventID] = rejection.Reason
	}
	distinct := make(map[string]struct{}, len(eventIDs))
	for _, id := range eventIDs {
		given, named := reasons[id]
		if !named || given == "" || (len(distinct) > 0 && given != reason) {
			return "", false
		}
		reason = given
		distinct[id] = struct{}{}
	}
	if len(distinct) < 2 {
		return "", false
	}
	return reason, true
}
