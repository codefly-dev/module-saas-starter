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
