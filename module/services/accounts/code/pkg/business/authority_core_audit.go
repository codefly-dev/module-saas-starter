package business

import (
	"context"
	"errors"
	"fmt"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/google/uuid"
)

// authorityEvents maps each change the core journals onto this host's audit
// record. A module's delivery and withdrawal are recorded by the delivery
// path (saas.solution.host_binding_applied), so they are not recorded twice.
var authorityEvents = map[domain.EventKind]struct {
	event    EventType
	resource string
}{
	domain.InstallationRequested:   {EventAuthorityInstallationRequested, "installation"},
	domain.InstallationActivated:   {EventAuthorityInstallationActivated, "installation"},
	domain.InstallationUninstalled: {EventAuthorityInstallationUninstalled, "installation"},
	domain.RoleGranted:             {EventAuthorityRoleGranted, "installation"},
	domain.RoleRevoked:             {EventAuthorityRoleRevoked, "installation"},
	domain.DelegationGranted:       {EventAuthorityDelegationGranted, "delegation"},
	domain.DelegationRevoked:       {EventAuthorityDelegationRevoked, "delegation"},
	domain.ResourceAttached:        {EventAuthorityResourceAttached, "installation"},
	domain.ResourceDetached:        {EventAuthorityResourceDetached, "installation"},
	domain.ResourceTransferred:     {EventAuthorityResourceTransferred, "installation"},
}

// boundTx is the core's transaction as this host's store opened it: the
// context its own writes bind to.
type boundTx interface{ Context() context.Context }

// journalAuthority records one change of the core in the transaction that
// makes it. The record is platform-scoped, the organization named in the
// payload: the core's transaction is the control plane's, which writes no
// tenant fan-out, and the core decides across organizations.
func (s *Service) journalAuthority(_ context.Context, tx authority.Tx, e domain.Event) error {
	mapped, recorded := authorityEvents[e.Kind]
	if !recorded {
		return nil
	}
	bound, ok := tx.(boundTx)
	if !ok {
		return errors.New("the authority core's transaction binds no context to record its audit in")
	}
	payload := map[string]any{"subject": e.Subject}
	if e.Organization != "" {
		payload["organization"] = e.Organization
	}
	if e.Actor != "" {
		payload["actor"] = e.Actor
	}
	for key, value := range e.Detail {
		if value != "" {
			payload[key] = value
		}
	}
	actorID, actorType := "", "system"
	if _, err := uuid.Parse(e.Actor); err == nil {
		actorID, actorType = e.Actor, "user"
	}
	if err := s.emitTx(bound.Context(), actorID, actorType, mapped.event, mapped.resource, e.Subject, "", payload); err != nil {
		return fmt.Errorf("record %s: %w", mapped.event, err)
	}
	return nil
}

// RecordAuthorityIssue records a capability the core issued, in a transaction
// of the organization it was issued in, before it is handed out: a child hop
// is a child session, every other shape a task started. An issue whose record
// cannot be written is refused.
func (s *Service) RecordAuthorityIssue(ctx context.Context, organization, actor string, child bool, task string) error {
	event := EventWorkContextTaskStarted
	if child {
		event = EventWorkContextChildSession
	}
	actorID, actorType := "", "system"
	if _, err := uuid.Parse(actor); err == nil {
		actorID, actorType = actor, "user"
	}
	if _, err := uuid.Parse(organization); err != nil {
		return fmt.Errorf("record %s: organization %q is not one of this host's", event, organization)
	}
	return s.store.WithOrgTx(ctx, organization, func(ctx context.Context) error {
		return s.emitTx(ctx, actorID, actorType, event, "work_context", task, organization)
	})
}
