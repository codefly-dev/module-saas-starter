package business

import (
	"context"
	"time"
)

// ExecutionCustodyRecord is private Accounts data. Envelope contains the entire
// immutable registration, including bearer material, encrypted by SecretCipher.
// Never project this record through a tenant API or telemetry.
type ExecutionCustodyRecord struct {
	Reference   string
	OrgID       string
	OwnerID     string
	AdmissionID string
	Fingerprint string
	Envelope    string
	ExpiresAt   time.Time
}

// ExecutionCustodyStore owns insert-once durability. An existing immutable
// registration is returned unchanged, including after an uncertain commit.
type ExecutionCustodyStore interface {
	RegisterExecutionCustody(context.Context, ExecutionCustodyRecord) (ExecutionCustodyRecord, error)
	FindExecutionCustody(context.Context, string, string, string) (ExecutionCustodyRecord, error)
	GetExecutionCustody(context.Context, string) (ExecutionCustodyRecord, error)
	PurgeExecutionCustody(context.Context, time.Time) error
}
