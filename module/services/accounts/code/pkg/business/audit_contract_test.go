package business

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// bestEffortEmitter implements only the fire-and-forget half of the audit
// contract — the shape a production service must never be able to serve behind.
type bestEffortEmitter struct{ emitted int }

func (e *bestEffortEmitter) Emit(context.Context, AuditEntry) { e.emitted++ }

// transactionalEmitter implements the full contract.
type transactionalEmitter struct {
	emitted   int
	committed int
	err       error
}

func (e *transactionalEmitter) Emit(context.Context, AuditEntry) { e.emitted++ }

func (e *transactionalEmitter) EmitTx(context.Context, AuditEntry) error {
	e.committed++
	return e.err
}

func TestVerifyAuditWiring_RejectsAnEmitterThatCannotJoinTheTransaction(t *testing.T) {
	service := &Service{}
	require.Error(t, service.VerifyAuditWiring(), "no emitter at all must fail startup")

	service.SetAuditEmitter(&bestEffortEmitter{})
	require.ErrorContains(t, service.VerifyAuditWiring(), "TxAuditEmitter",
		"an emitter without EmitTx must fail startup rather than downgrade security writes")

	service.SetAuditEmitter(&transactionalEmitter{})
	require.NoError(t, service.VerifyAuditWiring())
}

// A best-effort emitter must not quietly satisfy a security write: the emit has
// to fail so the mutation rolls back, rather than being written outside the
// caller's transaction where a crash would separate it from the change.
func TestEmitTx_RefusesToDowngradeToABestEffortWrite(t *testing.T) {
	emitter := &bestEffortEmitter{}
	service := &Service{}
	service.SetAuditEmitter(emitter)

	err := service.emitTx(t.Context(), "actor", "user", EventRoleCreated, "role", "role-1", "org-1")
	require.Error(t, err)
	require.Zero(t, emitter.emitted, "the entry must not be written outside the caller's transaction")
}

func TestEmitTx_PropagatesTheEmitterFailure(t *testing.T) {
	emitter := &transactionalEmitter{err: errors.New("audit backend unavailable")}
	service := &Service{}
	service.SetAuditEmitter(emitter)

	require.ErrorContains(t,
		service.emitTx(t.Context(), "actor", "user", EventRoleCreated, "role", "role-1", "org-1"),
		"audit backend unavailable")
	require.Equal(t, 1, emitter.committed)
}
