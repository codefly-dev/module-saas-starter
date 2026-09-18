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

// ExecutionCustodyExchange names one child the custody broker minted, for the
// audit spine. It holds identifiers only. A token, a nonce and the child's scope
// resource ids never belong here: the record is exported and published.
type ExecutionCustodyExchange struct {
	OrgID            string
	OwnerPrincipalID string
	// ActorPrincipalID is the child's current actor: the outermost delegated
	// principal, or the owner when the parent carries no actor chain.
	ActorPrincipalID string
	Audience         string
	// Operation is the installed operation key, empty under a legacy policy.
	Operation string
	Lookup    bool
	Consumer  string
	Reference string
	TaskID    string
	ExpiresAt time.Time
}

// ExecutionCustodyAudit is the broker's seam onto the audit spine.
type ExecutionCustodyAudit interface {
	RecordExecutionCustodyExchange(context.Context, ExecutionCustodyExchange) error
}

// RecordExecutionCustodyExchange commits the durable record of a child the
// custody broker minted. Like the module capability mint it is written after
// the credential exists, and the broker withholds the child when the record
// cannot be committed — so the spine never misses an issuance that happened.
//
// The owner is absent from a broker exchange, so the row's actor is the child's
// current actor rather than a session: the delegated agent, or the owner when
// the parent carries no actor chain. The owner rides in the payload either way.
func (s *Service) RecordExecutionCustodyExchange(ctx context.Context, exchange ExecutionCustodyExchange) error {
	// emitTx is a no-op without an emitter, which here would hand out an
	// unrecorded credential. Boot verifies the wiring; this keeps a host that
	// skipped that step from minting at all.
	if err := s.VerifyAuditWiring(); err != nil {
		return err
	}
	actor := AuditActor{ID: exchange.ActorPrincipalID, Type: ActorTypeAgent}
	if exchange.ActorPrincipalID == exchange.OwnerPrincipalID {
		actor.Type = ActorTypeUser
	}
	if err := actor.validate(); err != nil {
		return err
	}
	payload := map[string]any{
		"owner_principal_id": exchange.OwnerPrincipalID,
		"actor_principal_id": exchange.ActorPrincipalID,
		"audience":           exchange.Audience,
		"lookup":             exchange.Lookup,
		"consumer":           exchange.Consumer,
		"task_id":            exchange.TaskID,
		"expires_at":         exchange.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if exchange.Operation != "" {
		payload["operation"] = exchange.Operation
	}
	// The organization comes from the verified custody record, never the
	// request, and the row is that tenant's own: no control-plane reach needed.
	return s.store.WithOrgTx(ctx, exchange.OrgID, func(ctx context.Context) error {
		return s.emitTx(ctx, actor.ID, actor.Type, EventWorkContextAudienceExch, "execution_custody", exchange.Reference,
			exchange.OrgID, payload)
	})
}

var _ ExecutionCustodyAudit = (*Service)(nil)
