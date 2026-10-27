package business

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	domain.DelegationGranted:       {EventModuleDelegationGranted, "module_delegation"},
	domain.DelegationRevoked:       {EventModuleDelegationRevoked, "module_delegation"},
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
	payload, err := authorityPayload(tx, e)
	if err != nil {
		return err
	}
	actorID, actorType := authorityActor(e.Actor)
	if err := s.emitTx(bound.Context(), actorID, actorType, mapped.event, mapped.resource, e.Subject, "", payload); err != nil {
		return fmt.Errorf("record %s: %w", mapped.event, err)
	}
	return nil
}

// authorityPayload is a change's audit payload. A delegation's is the record of
// what was delegated, to which module and by whom, as delegations to a module
// have always been recorded; every other change names where it happened, who
// acted, what changed and the change's own details.
func authorityPayload(tx authority.Tx, e domain.Event) (map[string]any, error) {
	if e.Kind == domain.DelegationGranted || e.Kind == domain.DelegationRevoked {
		d, err := tx.Delegation(e.Subject)
		if err != nil {
			return nil, fmt.Errorf("read delegation %s to record it: %w", e.Subject, err)
		}
		scopes := make([]string, 0, len(d.Scopes))
		for _, sc := range d.Scopes {
			entry := sc.GetResourceKind() + ":" + strings.Join(sc.GetActions(), ",")
			if len(sc.GetResourceIds()) > 0 {
				entry += "[" + strings.Join(sc.GetResourceIds(), ",") + "]"
			}
			scopes = append(scopes, entry)
		}
		payload := map[string]any{"delegation_id": d.ID, "grantor_id": d.Grantor, "module": d.Module, "scopes": scopes}
		if e.Kind == domain.DelegationRevoked {
			payload["revoked_by"] = e.Actor
		}
		return payload, nil
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
	return payload, nil
}

// authorityActor is who an authority change or issue is recorded as: a person
// (a user id) as themselves, anything else (a module's workload, the host's own
// setup) as the system, its name kept in the payload.
func authorityActor(actor string) (id, actorType string) {
	if _, err := uuid.Parse(actor); err == nil {
		return actor, ActorTypeUser
	}
	return "", ActorTypeSystem
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
	actorID, actorType := authorityActor(actor)
	if _, err := uuid.Parse(organization); err != nil {
		return fmt.Errorf("record %s: organization %q is not one of this host's", event, organization)
	}
	return s.store.WithOrgTx(ctx, organization, func(ctx context.Context) error {
		return s.emitTx(ctx, actorID, actorType, event, "work_context", task, organization)
	})
}
