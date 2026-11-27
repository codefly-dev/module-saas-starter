package business

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
)

// authorityEvents maps each change the core journals onto this host's audit
// record. A module's delivery and withdrawal are recorded by the delivery
// path (saas.solution.host_binding_applied), so they are not recorded twice.
var authorityEvents = map[domain.EventKind]struct {
	event    EventType
	resource string
}{
	domain.InstallationRequested:        {EventAuthorityInstallationRequested, "installation"},
	domain.InstallationActivated:        {EventAuthorityInstallationActivated, "installation"},
	domain.InstallationUninstalled:      {EventAuthorityInstallationUninstalled, "installation"},
	domain.InstallationReconverged:      {EventAuthorityInstallationReconverged, "installation"},
	domain.InstallationUpgradeAvailable: {EventAuthorityInstallationUpgradeAvailable, "installation"},
	domain.InstallationUpgraded:         {EventAuthorityInstallationUpgraded, "installation"},
	domain.InstallationReachAdopted:     {EventAuthorityInstallationReachAdopted, "installation"},
	domain.InstallationReachUnreached:   {EventAuthorityInstallationReachUnreached, "installation"},
	domain.InstallationReachRestored:    {EventAuthorityInstallationReachRestored, "installation"},
	domain.RoleGranted:                  {EventAuthorityRoleGranted, "installation"},
	domain.RoleViewOpened:               {EventAuthorityRoleViewOpened, "installation"},
	domain.RoleRevoked:                  {EventAuthorityRoleRevoked, "installation"},
	domain.DelegationGranted:            {EventModuleDelegationGranted, "module_delegation"},
	domain.DelegationRevoked:            {EventModuleDelegationRevoked, "module_delegation"},
	domain.ResourceAttached:             {EventAuthorityResourceAttached, "installation"},
	domain.ResourceDetached:             {EventAuthorityResourceDetached, "installation"},
	domain.ResourceTransferred:          {EventAuthorityResourceTransferred, "installation"},
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

// recordAuthorityIssue stores only the verified decision kind, principal and
// audience before the authority hands out the capability.
func (s *Service) recordAuthorityIssue(ctx context.Context, d authority.Decision) error {
	event := EventWorkContextTaskStarted
	pageHop := d.Kind == authority.DecisionIssuePage && d.Actor != d.Owner
	if d.Kind == authority.DecisionIssueChild || pageHop {
		event = EventWorkContextChildSession
	}
	if _, err := uuid.Parse(d.Organization); err != nil {
		return fmt.Errorf("record %s: organization %q is not one of this host's", event, d.Organization)
	}
	payload := map[string]any{"decision": string(d.Kind), "audience": d.Audience}
	actorID, actorType := "", ActorTypeSystem
	if d.Kind == authority.DecisionIssuePerson || (d.Kind == authority.DecisionIssuePage && !pageHop) {
		actorID, actorType = authorityActor(d.Actor)
	} else if d.Actor != "" {
		// Installation and client principals may themselves be UUIDs.
		payload["actor"] = d.Actor
	}
	return s.store.WithOrgTx(ctx, d.Organization, func(ctx context.Context) error {
		return s.emitTx(ctx, actorID, actorType, event, "work_context", d.Task, d.Organization, payload)
	})
}

// ErrAuthorityDecisionUnrecorded reports an admitted issue this host's
// journal did not take: the capability is not handed out. It names no cause;
// the journal's own failure is logged where it happened.
var ErrAuthorityDecisionUnrecorded = errors.New("the issue could not be recorded in the audit journal, so it is not handed out")

type authorityProbeKey struct{}

// AuthorityProbe marks ctx as asking the core a question whose answer the
// asking operation discards: a mint held to what the core would issue, a
// source's governing scope checked against a role. The core decides as it
// would for a caller, and what it answers is no issue and no refusal of its
// own, so it is not a row of the journal. The operation that asked records
// what it did.
func AuthorityProbe(ctx context.Context) context.Context {
	return context.WithValue(ctx, authorityProbeKey{}, true)
}

// recordAuthorityDecision is the core's decision hook
// (authority.Config.Decisions, owner decision 11): each decision the core
// makes for a caller becomes a row of the journal this host already keeps.
//
//   - An admitted check or resource listing is not recorded: every served
//     call makes one.
//   - An admitted issue is the record it always was (recordAuthorityIssue: a
//     task started, a child session for one more hop), written in the
//     organization it was issued in before the capability is handed out. One
//     that cannot be written answers ErrAuthorityDecisionUnrecorded, and the
//     core then hands out nothing.
//   - A refusal is saas.authority.decision_refused in the organization it was
//     decided in, as the person who asked when one did: what was asked, and
//     why it was refused. It is an observation: the refusal stands whether or
//     not the row could be written, and a row that could not is logged by the
//     emitter.
//   - What a capability that did not verify says of itself (Decision.Claimed)
//     is proven by nothing. Its row is filed under no tenant, recorded as
//     nobody's, with the organization and the actor it claims in the payload;
//     so is a refusal naming no organization of this host.
func (s *Service) recordAuthorityDecision(ctx context.Context, d authority.Decision) error {
	if d.AdmittedCheck() {
		return nil
	}
	if probing, _ := ctx.Value(authorityProbeKey{}).(bool); probing {
		return nil
	}
	if d.Outcome == authority.Admitted {
		if err := s.recordAuthorityIssue(ctx, d); err != nil {
			wool.Get(ctx).In("recordAuthorityDecision").Warn("an admitted issue could not be recorded, so it is withheld",
				wool.Field("decision", string(d.Kind)), wool.Field("org", d.Organization), wool.Field("audience", d.Audience), wool.ErrField(err))
			return ErrAuthorityDecisionUnrecorded
		}
		return nil
	}
	payload := map[string]any{"decision": string(d.Kind), "code": d.Code, "reason": d.Reason}
	for name, value := range map[string]string{
		"owner": d.Owner, "actor": d.Actor, "audience": d.Audience, "binding": d.Binding, "operation": d.Operation,
		"method": d.Method, "installation": d.Installation, "task": d.Task, "delegation": d.Delegation,
	} {
		if value != "" {
			payload[name] = value
		}
	}
	if d.Advisory {
		payload["advisory"] = true
	}
	organization, actorID, actorType := d.Organization, "", ActorTypeSystem
	if _, err := uuid.Parse(organization); err != nil || d.Claimed {
		// Filed under no tenant: the organization is a claim, or none of
		// this host's, and stays in the payload as what was named.
		if organization != "" {
			payload["organization"] = organization
		}
		if d.Claimed {
			payload["claimed"] = true
		}
		organization = ""
	} else {
		actorID, actorType = authorityActor(d.Actor)
	}
	s.emit(ctx, actorID, actorType, EventAuthorityDecisionRefused, "authority_decision", d.Audience, organization, payload)
	return nil
}

// RecordAuthorityRefusal records a refusal this host made itself while
// deciding an issue, before the core was asked for it: a stated credential
// whose own grants hold nothing at the audience, or that is confined
// elsewhere. It is the row the core's own refusal would have been.
func (s *Service) RecordAuthorityRefusal(ctx context.Context, kind authority.DecisionKind, organization, person, audience, reason string) {
	_ = s.recordAuthorityDecision(ctx, authority.Decision{Kind: kind, Outcome: authority.Refused, Code: authority.CodeDenied, Reason: reason,
		Organization: organization, Owner: person, Actor: person, Audience: audience})
}

// DecisionHopForward names, in the journal, a refusal the hop's front door
// made itself: beside the core's own decision kinds, which it is none of.
const DecisionHopForward authority.DecisionKind = "hop"

// HopRefusal is one call the hop's front door refused itself, as the hop
// tells it (the hop library's forward.Refusal): the call, the refusal's code
// and the hop's own reason, the module addressed and what identifies the work.
// Claimed says the identity was read from a Work Context the authority did not
// check.
type HopRefusal struct {
	Method, Code, Reason                      string
	Module                                    string
	Organization, Owner, Actor, Binding, Task string
	Claimed                                   bool
}

// RecordHopRefusal records a refusal the hop made itself, which never reaches
// the core, as the row a refusal of the core's is (recordAuthorityDecision):
// in the organization of a call whose identity the authority checked, under
// no tenant for one read from an unverified Work Context or carrying none.
func (s *Service) RecordHopRefusal(ctx context.Context, r HopRefusal) {
	_ = s.recordAuthorityDecision(ctx, authority.Decision{Kind: DecisionHopForward, Outcome: authority.Refused, Code: r.Code, Reason: r.Reason,
		Organization: r.Organization, Owner: r.Owner, Actor: r.Actor, Audience: r.Module, Binding: r.Binding, Method: r.Method, Task: r.Task, Claimed: r.Claimed})
}
