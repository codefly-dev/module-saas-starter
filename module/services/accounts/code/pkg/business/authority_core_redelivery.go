package business

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/core/wool"
)

// A held generation that needs redelivery.
//
// Role names are admin and user only. The authority core refuses to read
// again a generation it holds that declares any other (decide.RoleNames), and
// what a store still holds under another name holds nothing
// (decide.HeldRoles): the solution is unavailable to people until its
// publisher delivers the two roles. That refusal is right, and it is not a
// reason for this host to fail its boot reconciliation start after start. So
// the reconciliation asks the core's own predicate before it reads a held
// generation again; one that declares another name is left as the core holds
// it, and its binding is reported by name as needing redelivery: logged, on
// the binding's own row (pending_reason) where an operator reads a delivery
// this host could not take, and in the generation trail. The reason stands
// until a new generation of the binding is applied.
type redeliveryNeeds struct {
	mu      sync.RWMutex
	reasons map[string]string // binding id -> reason
}

// needsRedelivery answers the reason binding's held generation needs
// redelivery, "" when the core reads it: m is the module as this host reads
// the delivery it holds.
func needsRedelivery(binding string, m domain.Module) string {
	refusal := decide.RoleNames(m)
	if refusal == nil {
		return ""
	}
	return fmt.Sprintf("generation %d of %s, held under binding %s, needs redelivery: %v. Role names are %q and %q only; until its publisher delivers them, the roles it holds under other names grant nothing and it is unavailable",
		m.Generation, m.Name, binding, refusal, domain.AdminRole, domain.UserRole)
}

// markNeedsRedelivery reports binding as needing redelivery for reason: once
// per start in the log, on the binding's row every time (its pending reason is
// overwritten by each pass's answer).
func (s *Service) markNeedsRedelivery(ctx context.Context, binding, reason string) error {
	s.redelivery.mu.Lock()
	known := s.redelivery.reasons[binding] == reason
	if s.redelivery.reasons == nil {
		s.redelivery.reasons = map[string]string{}
	}
	s.redelivery.reasons[binding] = reason
	s.redelivery.mu.Unlock()
	if !known {
		wool.Get(ctx).In("markNeedsRedelivery").Warn(reason, wool.Field("binding", binding))
	}
	err := s.recordSolutionHostBindingRefusal(ctx, binding, reason, time.Now())
	if err != nil {
		return fmt.Errorf("record that binding %s needs redelivery: %w", binding, err)
	}
	return nil
}

// clearNeedsRedelivery forgets binding's reason: a new generation of it was applied.
func (s *Service) clearNeedsRedelivery(binding string) {
	s.redelivery.mu.Lock()
	delete(s.redelivery.reasons, binding)
	s.redelivery.mu.Unlock()
}

// RedeliveryReasons are, per binding, why its held generation needs
// redelivery. A reconcile pass records each on its binding's row, so the
// reason outlives the pass that would otherwise clear a settled binding's.
func (s *Service) RedeliveryReasons() map[string]string {
	s.redelivery.mu.RLock()
	defer s.redelivery.mu.RUnlock()
	return maps.Clone(s.redelivery.reasons)
}
