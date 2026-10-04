package business

import (
	"context"
	"time"
)

// StoredAuditEvent is one copy of an event as a store of record holds it: the
// envelope, the retention class and details hash it was written with, and the
// full details the store keeps for it — in the events row for a security-class
// event, in the details table for a content-class one, and none for a
// content-class event past the content window.
type StoredAuditEvent struct {
	DeploymentID  string
	Entry         AuditEntry // the envelope; Payload is never set
	Retention     AuditRetentionClass
	DetailsSHA256 string
	Details       string
	HasDetails    bool
}

// AuditHistoryReader reads back what a store of record holds of this
// deployment's events that occurred in [from, to), across every organization
// and the platform, every copy of a duplicated event included — the read-back
// the history copy verifies against (ADR 0009, item 7). Like every read of a
// store of record it runs no DML; on BigQuery it is a Storage Read API session.
type AuditHistoryReader interface {
	ReadStoredAuditEvents(ctx context.Context, from, to time.Time, visit func(StoredAuditEvent) error) error
}
