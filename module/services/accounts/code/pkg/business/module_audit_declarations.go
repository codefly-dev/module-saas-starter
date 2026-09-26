package business

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ModuleDeclareAuditEventTypes admits the audit event types a composed module
// declares for itself — the module-side twin of a solution's manifest
// declaration (solution_audit_events.go), for a producer that registers no
// frontend half and so has no manifest to carry one.
//
// It is the same admission, not a parallel one. prefix is the module's own
// prefix: the caller must be the principal derived from it, so a module
// declares only under its own name. The declarations are held to the same
// rules (ValidateAuditEventTypeDeclarations), and admitted by the same code
// under the same operator binding: a type is admitted only into a namespace
// the module's MODULE_PRINCIPALS entry lists, owned as `solution:<prefix>`, and
// a type may only grow. Emission then goes through EmitAuditEvent with
// `solution` = prefix, and the declared type is resolved by the one lookup
// every read, redaction, webhook and export path uses.
//
// Re-declaring what is already admitted writes nothing and records nothing, so
// a module may declare on every start. An admission that changed the registry
// is audited with the write that made it.
func (s *Service) ModuleDeclareAuditEventTypes(ctx context.Context, caller ModuleCaller, prefix string, declarations []AuditEventTypeDeclaration) ([]EventType, []string, error) {
	if _, err := s.moduleGrant(caller); err != nil {
		return nil, nil, err
	}
	if !callerIsModulePrefix(caller, prefix) {
		return nil, nil, status.Errorf(codes.PermissionDenied,
			"principal %s may declare audit event types only under its own prefix, not %q", caller.PrincipalID, prefix)
	}
	declared, err := ValidateAuditEventTypeDeclarations(prefix, declarations)
	if err != nil {
		return nil, nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if len(declared) == 0 {
		return nil, nil, nil
	}
	var (
		written   []EventType
		takenOver []string
	)
	err = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		if takenOver, written, err = s.admitDeclaredAuditEventTypesWritten(ctx, prefix, declared); err != nil {
			return err
		}
		if len(written) == 0 && len(takenOver) == 0 {
			return nil
		}
		types := make([]string, 0, len(written))
		for _, t := range written {
			types = append(types, string(t))
		}
		payload := map[string]any{"prefix": prefix, "event_types": types}
		if len(takenOver) > 0 {
			payload["namespaces_taken_over"] = takenOver
		}
		return s.emitTx(ctx, caller.PrincipalID, ActorTypeSystem,
			EventModuleAuditTypesDeclared, "module", prefix, "", payload)
	})
	switch {
	case err == nil:
		return written, takenOver, nil
	case errors.Is(err, ErrSolutionAuditDeclarationRejected):
		return nil, nil, status.Error(codes.InvalidArgument, err.Error())
	default:
		if _, ok := status.FromError(err); ok {
			return nil, nil, err
		}
		return nil, nil, status.Error(codes.Internal, "audit: cannot admit the declared event types")
	}
}

// callerIsModulePrefix reports whether the authenticated principal is the one
// derived from prefix — the same derivation the MODULE_PRINCIPALS registry is
// indexed by — compared in canonical form.
func callerIsModulePrefix(caller ModuleCaller, prefix string) bool {
	if prefix == "" {
		return false
	}
	own, err := uuid.Parse(caller.PrincipalID)
	if err != nil {
		return false
	}
	return own.String() == ModulePrincipalID(prefix)
}
