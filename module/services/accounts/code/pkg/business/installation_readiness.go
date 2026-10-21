package business

import (
	"context"
	"fmt"
	"slices"
	"strings"

	gen "accounts/pkg/gen/saas/accounts/v1"

	modulecontract "github.com/codefly-dev/core/contracts/module"
)

// Readiness is whether an installation WILL WORK, which is a different question
// from whether its record exists.
//
// InstallationHealth answers about the record: it is active, its owner of record
// is still an administrator, its standing grant is there. On a validation
// cell it answered HEALTHY for all ten installations while four declared
// operation bindings had derived nothing and no source could hold a delegation —
// so every failure surfaced hours later as a page that did not work. A health
// that cannot be false about the thing a person cares about is green about a
// name.
//
// Readiness is computed, never stored. Every fact it reads is one this host
// already holds and already reads for another purpose:
//
//   - the installation's lifecycle and its live health, from the listing itself;
//   - the applied module manifests, which say what each module ASKS for;
//   - the derived module registry, which says what it HOLDS;
//   - this organisation's sources and their active delegations.
//
// Nothing is asked of a module. That matters: if readiness needed a module to
// answer, it would be a protocol change on every module rather than a surface
// over the host's own facts, and would have to be argued as one.
//
// An omitted operation binding is reported by comparing the applied manifest's
// asks against the derived registry — the two published facts — rather than by
// re-running the derivation. The reconcile pass owns the derivation and names
// each omission in its own log (codefly-dev/module-saas-starter#1165); this read
// owns the comparison, so the page states what is missing even when nobody was
// watching the boot log.

// ReadinessCondition is one unmet condition: what is unmet, in the words of
// whoever owns the fact, who can clear it, and what clears it.
type ReadinessCondition struct {
	Kind      gen.ReadinessConditionKind
	Subject   string
	Detail    string
	ClearedBy gen.ReadinessClearedBy
	Action    gen.ReadinessAction
	SourceID  string
}

// InstallationReadiness is the verdict and its reasons. Ready is true only when
// Unmet is empty: a condition nobody can clear here still makes it false,
// because the module does not work either way.
type InstallationReadiness struct {
	Ready bool
	Unmet []ReadinessCondition
}

// ReachedModule is one module an installed target's applied manifest declares it
// reaches on the asking person's own session, resolved to this organisation's
// installation of that module — where the team grant is written.
type ReachedModule struct {
	Audience       string
	InstallationID string
	RouteAlias     string
	// InvokeScopes is what the solution declares it consumes there, as the
	// DERIVED registry holds it — the manifest's ask narrowed by the module
	// envelope, which is what the solution can actually do. A scope the envelope
	// withheld is not consumption, and readiness reports it as an omitted binding
	// rather than letting it travel here. This is the set a grant of the solution
	// CARRIES (derived_module_access.go).
	InvokeScopes []ModuleOperationScope
}

// readinessFacts is one snapshot of everything readiness is derived from, read
// once per listing rather than once per row.
type readinessFacts struct {
	// askedByBinding is the authority each applied, live manifest asks for,
	// keyed by the host binding whose manifest asks. An installation reaches it
	// through the binding its target records.
	askedByBinding map[string]ModuleAuthorityRequest
	// bindingByPrincipal is the reverse: which binding's manifest asks under a
	// module principal, so an audience resolves to an installed target.
	bindingByPrincipal map[string]string
	registry           ModulePrincipalRegistry
	// delegationTargets is the module binding of every declared module that
	// accepts source delegations.
	delegationTargets []SourceDelegationTarget
	sources           []*DatasourceSource
	// delegated is the set of "<source id>\x00<module prefix>" an active
	// delegation covers.
	delegated map[string]struct{}
}

// readReadinessFacts reads the organisation-scoped half of the facts. It runs
// inside the caller's read snapshot so the sources and the delegations describe
// one moment: a reconnect that commits between the two reads is seen by both or
// by neither, and a source is never reported undelegated against a delegation
// list read a moment later.
func (s *Service) readReadinessFacts(ctx context.Context, orgID string) (*readinessFacts, error) {
	facts := &readinessFacts{
		askedByBinding:     map[string]ModuleAuthorityRequest{},
		bindingByPrincipal: map[string]string{},
		registry:           s.declaredModules(),
		delegated:          map[string]struct{}{},
	}
	facts.delegationTargets = facts.registry.SourceDelegationTargets()
	sources, err := s.store.ListDatasourceSources(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("this organisation's sources: %w", err)
	}
	facts.sources = sources
	delegations, err := s.store.ListSourceDelegations(ctx, orgID, "", false)
	if err != nil {
		return nil, fmt.Errorf("the active source delegations: %w", err)
	}
	for _, delegation := range delegations {
		if delegation.Active() {
			facts.delegated[delegation.SourceID+"\x00"+delegation.ModulePrefix] = struct{}{}
		}
	}
	return facts, nil
}

// withAppliedManifests adds the control-plane half: what each applied, live
// manifest asks for. Presence is control-plane state, which is why the caller
// reads it there, beside the catalogue rows it reads for the same page.
func (f *readinessFacts) withAppliedManifests(applied []*SolutionHostBindingRecord) {
	for _, request := range AppliedAuthorityRequests(applied) {
		f.askedByBinding[request.Binding] = request
		if request.Authority != nil && request.Authority.Principal != "" {
			f.bindingByPrincipal[request.Authority.Principal] = request.Binding
		}
	}
}

// sourceLabel is how a source is named to a person: the repository or address it
// was connected as, falling back to its provider. Never its id alone — a person
// asked to reconnect a source must recognise which one.
func sourceLabel(source *DatasourceSource) string {
	if source.Repo != "" {
		return source.Repo
	}
	if source.BoundaryLabel != "" {
		return source.BoundaryLabel
	}
	if source.Provider != "" {
		return source.Provider
	}
	return source.ID
}

// readinessFor is one installation's verdict.
//
// installedByPrincipal maps a module principal to this organisation's active
// installation of it, built from the rows of the same listing: a reached module
// is offered a grant only where the organisation actually installed it.
func (f *readinessFacts) readinessFor(
	installation *gen.Installation, health gen.InstallationHealth,
	target *AvailableSolutionTarget, installedByPrincipal map[string]*gen.Installation,
) (InstallationReadiness, []ReachedModule) {
	var conditions []ReadinessCondition
	add := func(c ReadinessCondition) { conditions = append(conditions, c) }

	switch installation.GetStatus() {
	case gen.InstallationStatus_INSTALLATION_STATUS_PENDING:
		add(ReadinessCondition{
			Kind:      gen.ReadinessConditionKind_READINESS_CONDITION_KIND_INSTALL_STEP_PENDING,
			Detail:    "The solution has not acknowledged its install step yet. Nothing can use the installation until it does.",
			ClearedBy: gen.ReadinessClearedBy_READINESS_CLEARED_BY_NOBODY,
			Action:    gen.ReadinessAction_READINESS_ACTION_WAIT,
		})
	case gen.InstallationStatus_INSTALLATION_STATUS_FAILED:
		detail := installation.GetFailureReason()
		if detail == "" {
			detail = "The install step did not succeed, and the host recorded no reason."
		}
		add(ReadinessCondition{
			Kind:      gen.ReadinessConditionKind_READINESS_CONDITION_KIND_INSTALL_STEP_FAILED,
			Detail:    detail,
			ClearedBy: gen.ReadinessClearedBy_READINESS_CLEARED_BY_ORGANIZATION_ADMIN,
			Action:    gen.ReadinessAction_READINESS_ACTION_RETRY_INSTALL_STEP,
		})
	case gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE:
		if condition, unmet := recordCondition(health); unmet {
			add(condition)
		}
	default:
	}

	// Beyond the record: what the module asked for and what it holds. Only an
	// active installation is judged on these — a pending or failed one is not
	// serving anything yet, and listing its module conditions beside "waiting
	// for the install step" would read as more wrong than it is.
	var reached []ReachedModule
	if installation.GetStatus() != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE || target == nil {
		return verdict(conditions), reached
	}
	asked, known := f.askedByBinding[target.BindingID]
	if !known || asked.Authority == nil {
		return verdict(conditions), reached
	}
	prefix := asked.Authority.Principal
	grant, holds := f.registry[ModulePrincipalID(prefix)]
	if !holds {
		add(ReadinessCondition{
			Kind:    gen.ReadinessConditionKind_READINESS_CONDITION_KIND_MODULE_PRINCIPAL_OMITTED,
			Subject: prefix,
			Detail: fmt.Sprintf("The module registry holds no principal %q, so every Work Context it asks for is denied. "+
				"A principal is in the registry only while an applied manifest asks for it and the module envelope allows it.", prefix),
			ClearedBy: gen.ReadinessClearedBy_READINESS_CLEARED_BY_PLATFORM_OPERATOR,
			Action:    gen.ReadinessAction_READINESS_ACTION_NONE_AT_THIS_SURFACE,
		})
		return verdict(conditions), reached
	}

	seen := map[string]int{}
	for _, binding := range asked.Authority.Bindings {
		if derivedAudience, derived := grant.OperationAudiences[binding.ID]; derived {
			// Only an invoke acts on the asking person's session, so only an
			// invoke needs that person to hold a grant on the module. A
			// headless-only binding is background work no person asks for and
			// would be a grant nobody needs.
			if len(binding.Operations[modulecontract.OperationInvoke]) == 0 {
				continue
			}
			// Two bindings may redeem on the same audience. The person needs
			// what EITHER of them does on their session, so the declared
			// consumption of that module is the union across its bindings
			// rather than whichever one was listed first.
			if index, already := seen[binding.Audience]; already {
				reached[index].InvokeScopes = append(reached[index].InvokeScopes, derivedAudience.InvokeScopes...)
				continue
			}
			seen[binding.Audience] = len(reached)
			entry := f.reachedModule(binding.Audience, installedByPrincipal)
			entry.InvokeScopes = slices.Clone(derivedAudience.InvokeScopes)
			reached = append(reached, entry)
			continue
		}
		add(ReadinessCondition{
			Kind:    gen.ReadinessConditionKind_READINESS_CONDITION_KIND_OPERATION_BINDING_OMITTED,
			Subject: binding.ID,
			Detail: fmt.Sprintf("The applied manifest of %q asks for operation binding %q on audience %q and the host derived nothing for it: "+
				"the module envelope allows no such binding, or allows nothing of it. Until the envelope names it, every exchange under it is refused.",
				prefix, binding.ID, binding.Audience),
			ClearedBy: gen.ReadinessClearedBy_READINESS_CLEARED_BY_PLATFORM_OPERATOR,
			Action:    gen.ReadinessAction_READINESS_ACTION_NONE_AT_THIS_SURFACE,
		})
	}

	for _, entry := range reached {
		if entry.InstallationID == "" {
			add(ReadinessCondition{
				Kind:    gen.ReadinessConditionKind_READINESS_CONDITION_KIND_REACHED_MODULE_NOT_INSTALLED,
				Subject: entry.Audience,
				Detail: fmt.Sprintf("This target reaches %q on the asking person's own session, and this organisation has not installed %q. "+
					"Every call it makes there is refused.", entry.Audience, entry.Audience),
				ClearedBy: gen.ReadinessClearedBy_READINESS_CLEARED_BY_ORGANIZATION_ADMIN,
				Action:    gen.ReadinessAction_READINESS_ACTION_INSTALL_REACHED_MODULE,
			})
		}
	}

	// A source with no active delegation to this module. Only a module that
	// DECLARES a source-delegation binding is judged on it: a module that accepts
	// no delegation is not missing one.
	for _, delegation := range f.delegationTargets {
		if delegation.Prefix != prefix {
			continue
		}
		for _, source := range f.sources {
			if _, ok := f.delegated[source.ID+"\x00"+prefix]; ok {
				continue
			}
			add(ReadinessCondition{
				Kind:     gen.ReadinessConditionKind_READINESS_CONDITION_KIND_SOURCE_NOT_DELEGATED,
				Subject:  sourceLabel(source),
				SourceID: source.ID,
				Detail: fmt.Sprintf("The source %q has no active delegation to %q, so %q cannot read it on anyone's behalf. "+
					"The delegation is the connecting person's: a person must connect or reconnect the source.",
					sourceLabel(source), prefix, prefix),
				ClearedBy: gen.ReadinessClearedBy_READINESS_CLEARED_BY_VIEWER,
				Action:    gen.ReadinessAction_READINESS_ACTION_RECONNECT_SOURCE,
			})
		}
	}
	return verdict(conditions), reached
}

// reachedModule resolves one declared audience to this organisation's
// installation of that module, or to no installation at all.
func (f *readinessFacts) reachedModule(audience string, installed map[string]*gen.Installation) ReachedModule {
	entry := ReachedModule{Audience: audience}
	if installation, ok := installed[audience]; ok {
		entry.InstallationID = installation.GetId()
	}
	return entry
}

// recordCondition restates a not-HEALTHY InstallationHealth as a condition, in
// the health enum's own words, with who repairs it. The health value stays on
// the row; this puts it in the one list a reader works through.
func recordCondition(health gen.InstallationHealth) (ReadinessCondition, bool) {
	condition := ReadinessCondition{
		Kind:      gen.ReadinessConditionKind_READINESS_CONDITION_KIND_RECORD_UNHEALTHY,
		ClearedBy: gen.ReadinessClearedBy_READINESS_CLEARED_BY_ORGANIZATION_ADMIN,
		Action:    gen.ReadinessAction_READINESS_ACTION_NONE_AT_THIS_SURFACE,
	}
	switch health {
	case gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY:
		return ReadinessCondition{}, false
	case gen.InstallationHealth_INSTALLATION_HEALTH_NO_ELIGIBLE_OWNER:
		condition.Detail = "Neither the owner of record nor any co-owner is an organisation administrator any more, " +
			"so no task can be minted for this installation. Transferring it to a current administrator restores it."
		condition.Action = gen.ReadinessAction_READINESS_ACTION_TRANSFER_OWNERSHIP
	case gen.InstallationHealth_INSTALLATION_HEALTH_AGENT_REVOKED:
		condition.Detail = "The installation's agent principal is revoked. Nothing can act as this installation until it is reinstalled."
	case gen.InstallationHealth_INSTALLATION_HEALTH_AGENT_DISABLED:
		condition.Detail = "The installation's agent principal is disabled. It acts again once an administrator re-enables that principal."
	case gen.InstallationHealth_INSTALLATION_HEALTH_STANDING_GRANT_MISSING:
		condition.Detail = "The agent's own standing grant at the installation's authority root is gone, " +
			"so it holds no authority there even though the record is active."
	default:
		condition.Detail = "This host did not report the installation's health, so whether it can act is unknown."
	}
	return condition, true
}

// verdict sorts the conditions into one stable order — the record first, then
// what the module asked for, then its sources — and answers ready only on an
// empty list.
func verdict(conditions []ReadinessCondition) InstallationReadiness {
	slices.SortStableFunc(conditions, func(a, b ReadinessCondition) int {
		if a.Kind != b.Kind {
			return int(a.Kind) - int(b.Kind)
		}
		return strings.Compare(a.Subject, b.Subject)
	})
	return InstallationReadiness{Ready: len(conditions) == 0, Unmet: conditions}
}
