package business

import (
	"context"
	"fmt"
	"slices"
	"strings"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// A solution's grant CARRIES the module access the solution declares it consumes
// (codefly-dev/module-saas-starter#1167).
//
// A solution reaches other modules on the ASKING PERSON'S OWN SESSION: the
// consumer-invoke ceiling bounds what the solution may do there, but the person
// still has to hold authority on the reached module, or the call is refused. On
// a validation cell a team granted one solution was then refused by the FOUR
// modules that solution reaches, in turn — four separate dialogs, each found by
// hitting a `Forbidden`. Nothing anywhere said which modules a solution reaches.
// (Which solution and which modules is in the issue, not here: this host has no
// build-time knowledge of either.)
//
// The declaration that says so already exists and is already the thing the
// passthrough is checked against, so installing the solution is the
// administrator consenting to exactly it. What was missing is that the consent
// was carried nowhere.
//
// # The shape, and the one it is not
//
// A grant of a solution to a team now derives, per module that solution reaches
// and this organisation has installed, a role THIS HOST OWNS whose permissions
// are the solution's declared consumption for that module INTERSECTED WITH the
// role the administrator chose — and grants that role to the team at the reached
// module's own authority-root node.
//
// The alternative was to write the administrator's chosen role at each reached
// module's root. It is cheaper and it was rejected: it would WIDEN the person's
// own direct reach into that module to whatever the role permits there. They
// could call the module directly, not only through the solution. The solution's
// own use stays fenced by its consumer-invoke ceiling; the person's would not.
// What the derived role buys is exactly that property — a person's direct reach
// into a reached module is what the SOLUTION declared, never the administrator's
// full role.
//
// # Where it is recorded
//
// As an ordinary scope grant at the reached module's authority-root node. That
// node is the only thing that makes a module reachable, and the grant is
// resolved by the same ListAccessibleScopes / CheckAccess union as every other
// grant, so there is no second answer to "what does this viewer reach" and no
// side table that could disagree with one. The one thing that distinguishes it
// is `scope_grants.derived_from_grant_id`, which names the grant that caused it.
//
// # How it is revoked, with its cause
//
// RevokeInstallationFromTeam revokes every derived grant of the grant it is
// removing, in the same narrowing transaction, before the cause goes.
// UninstallSolution revokes every derived grant whose cause is a grant at that
// installation's authority root. Both go through revokeScopeApply, so each
// carries the same scope.revoked event and audit record as any other
// revocation — a person reading the record sees why it went. The column's
// ON DELETE CASCADE closes the paths that do NOT go through either (a bare
// RevokeScope of the cause, an organisation deletion), so a derived grant can
// never outlive its cause even where nothing thought to remove it.
//
// A derived grant ADDS. A person's own direct grant on a reached module is a
// different row, with no cause, and it survives the revocation of the solution's
// grant untouched.
//
// # Reconciliation
//
// The derived set is a function of the applied declaration, which a later
// generation may move. It is reconciled on the NEXT grant or revoke of its
// cause — EnsureDerivedRole replaces the permission set, and a module no longer
// reached simply stops being re-derived. There is NO background pass: an
// applied generation that narrows a declaration does not narrow the carried
// grants until the cause is granted or revoked again.

// DerivedRole is a role this host derives and owns. It is keyed, named and
// reconciled by DerivedFrom, which is its provenance: the installation it was
// derived from, the module audience it is for, and the chosen role it was
// intersected with.
type DerivedRole struct {
	OrgID       string
	DerivedFrom string
	Name        string
	Description string
	Permissions []*gen.Permission
}

// CarriedModuleAccess is one derived grant a solution's grant carried.
type CarriedModuleAccess struct {
	Audience       string
	InstallationID string
	ScopePath      string
	RoleID         string
	// Scopes is what the derived role permits, each "<resource kind>:<action>".
	Scopes []string
}

// InstallationGrant is the whole outcome of exposing an installation to a team:
// the exposure grant, what it carried, and where it carried nothing and why.
//
// The refusals travel WITH the grant rather than being discoverable later. A
// refusal a person meets as a `Forbidden` on a page is the failure this replaces,
// so a derivation that could not happen is stated in the same response, in the
// same condition vocabulary the setup page reads.
type InstallationGrant struct {
	Grant   *gen.ScopeGrant
	Carried []CarriedModuleAccess
	Unmet   []ReadinessCondition
}

// derivedRoleProvenance is the key and the recorded provenance of one derived
// role. Every field is part of the identity: two teams granted the same solution
// at different roles get different intersections, so they cannot share a role.
func derivedRoleProvenance(installationID, audience, chosenRoleID string) string {
	return fmt.Sprintf("installation=%s;audience=%s;role=%s", installationID, audience, chosenRoleID)
}

// derivedRoleName is the role's name, and it is the provenance rather than
// anything prettier for one reason: it must never need rewriting.
//
// `app_tenant` holds UPDATE on roles.description alone, column-scoped on purpose
// (migration 5, and TestTenantRoleColumnGrantsAreLimitedToDescription holds it
// there) because a table-wide UPDATE would let a tenant re-parent a global role.
// A name carrying the solution's route alias would have to be refreshed when a
// later applied generation moved that alias, which would need UPDATE on `name`
// too — widening a deliberate hardening to make a label read better. So the
// alias lives in the DESCRIPTION, which is refreshable, and the name is the
// stable triple. (org_id, name) is unique, and the provenance already is.
func derivedRoleName(provenance string) string {
	return "carried module access · " + provenance
}

// carriedModuleAccessPlan is what a grant must carry at ONE reached module.
type carriedModuleAccessPlan struct {
	audience       string
	installationID string
	// declared is the solution's declared consumption on that module's invoke
	// operations, as the derived registry holds it.
	declared []ModuleOperationScope
}

// planCarriedModuleAccess splits the modules a target reaches into the ones a
// grant can carry access for and the ones it cannot, with the reason for each
// refusal. Pure: it decides nothing that needs the database.
//
// Two refusals, and both grant nothing rather than approximating:
//
//   - The module is not installed here. There is nothing to grant at, and the
//     listing's readiness says the same thing about the same installation; it is
//     restated here because this response is what the administrator is reading.
//   - A declared scope is narrowed to NAMED RESOURCES. `role_permissions` is
//     (resource, action) and has no resource-id column, so a role cannot express
//     "these three records of this kind". Carrying it as the whole kind would
//     hand the person more than the solution declared, which is the one thing
//     this derivation exists to prevent, so the whole audience is refused and
//     said out loud. Representing it needs a new authorization record shape,
//     which is a contract decision rather than something to improvise here.
func planCarriedModuleAccess(reached []ReachedModule) ([]carriedModuleAccessPlan, []ReadinessCondition) {
	var plans []carriedModuleAccessPlan
	var unmet []ReadinessCondition
	for _, module := range reached {
		if module.InstallationID == "" {
			unmet = append(unmet, ReadinessCondition{
				Kind:    gen.ReadinessConditionKind_READINESS_CONDITION_KIND_REACHED_MODULE_NOT_INSTALLED,
				Subject: module.Audience,
				Detail: fmt.Sprintf("This solution reaches %q on the asking person's own session and this organisation "+
					"has not installed %q, so the grant carried no access there and every call it makes is refused.",
					module.Audience, module.Audience),
				ClearedBy: gen.ReadinessClearedBy_READINESS_CLEARED_BY_ORGANIZATION_ADMIN,
				Action:    gen.ReadinessAction_READINESS_ACTION_INSTALL_REACHED_MODULE,
			})
			continue
		}
		if named := namedResourceScopes(module.InvokeScopes); len(named) > 0 {
			unmet = append(unmet, ReadinessCondition{
				Kind:    gen.ReadinessConditionKind_READINESS_CONDITION_KIND_CARRIED_MODULE_ACCESS_NOT_DERIVED,
				Subject: module.Audience,
				Detail: fmt.Sprintf("This solution's declared consumption of %q is narrowed to named resources (%s), "+
					"and a role is (resource, action) with no resource-id: carrying it would grant the whole kind, "+
					"which is more than the solution declared. Nothing was carried for %q, so the team must be granted "+
					"it directly or the declaration must widen to the kind.",
					module.Audience, strings.Join(named, ", "), module.Audience),
				ClearedBy: gen.ReadinessClearedBy_READINESS_CLEARED_BY_PLATFORM_OPERATOR,
				Action:    gen.ReadinessAction_READINESS_ACTION_NONE_AT_THIS_SURFACE,
			})
			continue
		}
		// A module declared with NO scopes is still reached, and still needs a
		// grant.
		//
		// This used to `continue` here, silently. The case is a module a
		// solution calls on the viewer's own bearer and mints no authority
		// for — the declaration asks for nothing because there is nothing to
		// ask for, and the consuming runtime refuses to ask. But reaching it
		// at all is authorized at that module's installation, so with no grant
		// there every one of its routes is refused and the derivation said
		// nothing about a module it had quietly skipped.
		//
		// Measured on a validation cell: a solution reaching four modules
		// derived three grants and the fourth module — the one declared with
		// no scopes — had none, so its whole surface stayed dead with no
		// readiness condition naming it.
		//
		// It is carried with an empty declaration, which carryModuleAccessTx
		// turns into a role holding the reach permission alone.
		plans = append(plans, carriedModuleAccessPlan{
			audience: module.Audience, installationID: module.InstallationID, declared: module.InvokeScopes,
		})
	}
	return plans, unmet
}

// namedResourceScopes names the declared scopes a role cannot express, as
// "<kind> (<n> resources)", so the refusal says which part of the declaration
// is the problem rather than that there is one.
func namedResourceScopes(scopes []ModuleOperationScope) []string {
	var named []string
	for _, scope := range scopes {
		if len(scope.ResourceIDs) > 0 {
			named = append(named, fmt.Sprintf("%s (%d named resources)", scope.ResourceKind, len(scope.ResourceIDs)))
		}
	}
	return named
}

// declaredPairs flattens a declared scope set to the (resource, action) pairs a
// role is asked about, deduplicated and in a fixed order so the derived role's
// permission set does not depend on the order the declaration happened to list.
func declaredPairs(scopes []ModuleOperationScope) (resources, actions []string) {
	seen := map[string]struct{}{}
	for _, scope := range scopes {
		for _, action := range scope.Actions {
			key := scope.ResourceKind + ":" + action
			if _, already := seen[key]; already {
				continue
			}
			seen[key] = struct{}{}
			resources = append(resources, scope.ResourceKind)
			actions = append(actions, action)
		}
	}
	order := make([]int, len(resources))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		if c := strings.Compare(resources[a], resources[b]); c != 0 {
			return c
		}
		return strings.Compare(actions[a], actions[b])
	})
	sortedResources := make([]string, len(resources))
	sortedActions := make([]string, len(actions))
	for i, index := range order {
		sortedResources[i], sortedActions[i] = resources[index], actions[index]
	}
	return sortedResources, sortedActions
}

// carryModuleAccessTx derives and grants, inside the transaction that wrote the
// cause. Everything a grant carries is therefore in the cause's transaction: a
// solution grant that commits having carried nothing because a later statement
// failed is not a state this can reach.
func (s *Service) carryModuleAccessTx(
	ctx context.Context, actorID, orgID, installationID, teamID, alias, chosenRoleID string,
	cause *gen.ScopeGrant, plans []carriedModuleAccessPlan,
) ([]CarriedModuleAccess, []ReadinessCondition, error) {
	var carried []CarriedModuleAccess
	var unmet []ReadinessCondition
	for _, plan := range plans {
		resources, actions := declaredPairs(plan.declared)
		permitted, err := s.installationStore().RolePermittedScopes(ctx, chosenRoleID, resources, actions)
		if err != nil {
			return nil, nil, err
		}
		// Every derived role carries the REACH permission, before anything it
		// declares.
		//
		// Reaching a module through this host is authorized separately from
		// what the call then does: the proxy admits a module route only for a
		// viewer entitled to that module's installation, and entitlement is
		// (ResourceTypeSolution, ActionUseSolution) at the installation's own
		// authority root. The declared scopes answer "what may this call do",
		// never "may this caller reach it at all".
		//
		// Without it every derived grant was INERT: measured on a validation
		// cell, three derived grants carried exactly the declared module
		// scopes, read correctly in the administrator's listing, and every
		// route they were supposed to open answered Forbidden — because the
		// one permission that opens a module was in neither the declaration
		// nor the derivation.
		//
		// It is not taken from the chosen role and not intersected with it.
		// The administrator granting the solution is what authorizes reaching
		// what the solution reaches; that is the decision this derivation
		// exists to carry, and a chosen role that happens not to spell
		// `solution:use` is not a decision to withhold it.
		permissions := make([]*gen.Permission, 0, len(resources)+1)
		permissions = append(permissions, &gen.Permission{Resource: ResourceTypeSolution, Action: ActionUseSolution})
		scopes := make([]string, 0, len(resources)+1)
		scopes = append(scopes, ResourceTypeSolution+":"+ActionUseSolution)
		for i, permits := range permitted {
			if !permits {
				continue
			}
			permissions = append(permissions, &gen.Permission{Resource: resources[i], Action: actions[i]})
			scopes = append(scopes, resources[i]+":"+actions[i])
		}
		// An empty intersection grants NOTHING and says so. This repository
		// already refuses a role that would expose nothing for the same reason —
		// a grant that is written and reveals nothing is the one outcome an
		// administrator must never be handed — so the refusal reuses that error's
		// words rather than inventing a second vocabulary for the same mistake.
		// The reach permission is always present, so "carried nothing" is now
		// "carried nothing the solution DECLARED" — which is still worth
		// saying, and still not worth refusing, because the reach alone is
		// what a module declared with no scopes legitimately carries.
		if len(permissions) == 1 && len(resources) > 0 {
			unmet = append(unmet, ReadinessCondition{
				Kind:    gen.ReadinessConditionKind_READINESS_CONDITION_KIND_CARRIED_MODULE_ACCESS_NOT_DERIVED,
				Subject: plan.audience,
				Detail: fmt.Sprintf("%v: the chosen role permits none of what this solution declares it consumes on %q (%s), "+
					"so the grant carried nothing there and the pages that reach %q will be refused. "+
					"A role that permits some of it carries that much.",
					ErrRoleDoesNotExposeSolution, plan.audience, strings.Join(declaredLabels(resources, actions), ", "), plan.audience),
				ClearedBy: gen.ReadinessClearedBy_READINESS_CLEARED_BY_ORGANIZATION_ADMIN,
				Action:    gen.ReadinessAction_READINESS_ACTION_NONE_AT_THIS_SURFACE,
			})
			continue
		}
		// The reached installation's own authority root, resolved from the
		// installation under the same SHARE lock the exposure grant takes on its
		// own: a derived grant never lands at a module this organisation has
		// already uninstalled, and an uninstall of a reached module waits for
		// this grant to commit rather than racing it.
		path, err := s.installationStore().InstallationRootScopePath(ctx, orgID, plan.installationID)
		if err != nil {
			return nil, nil, err
		}
		provenance := derivedRoleProvenance(installationID, plan.audience, chosenRoleID)
		derivedRoleID, err := s.installationStore().EnsureDerivedRole(ctx, DerivedRole{
			OrgID:       orgID,
			DerivedFrom: provenance,
			Name:        derivedRoleName(provenance),
			Description: fmt.Sprintf("Derived by this host: what %q declares it consumes on %q, intersected with the role "+
				"an administrator granted it. Granted with that solution and revoked with it; never granted by hand.",
				solutionLabel(alias), plan.audience),
			Permissions: permissions,
		})
		if err != nil {
			return nil, nil, err
		}
		grant, err := s.grantScopeTx(ctx, actorID, &gen.GrantScopeRequest{
			OrgId:       orgID,
			SubjectId:   teamID,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM,
			ScopePath:   path,
			RoleId:      derivedRoleID,
		}, cause.GetId())
		if err != nil {
			return nil, nil, err
		}
		carried = append(carried, CarriedModuleAccess{
			Audience:       plan.audience,
			InstallationID: plan.installationID,
			ScopePath:      grant.GetScopePath(),
			RoleID:         derivedRoleID,
			Scopes:         scopes,
		})
	}
	return carried, unmet, nil
}

// solutionLabel is how the solution reads in the derived role's description: its
// current route alias, which is display only, with a fallback so a target whose
// catalogue row this host no longer offers still produces a sentence.
func solutionLabel(alias string) string {
	if alias == "" {
		return "this solution"
	}
	return alias
}

// declaredLabels renders a declared pair set as "<kind>:<action>" labels, the
// spelling the declaration itself uses.
func declaredLabels(resources, actions []string) []string {
	labels := make([]string, 0, len(resources))
	for i := range resources {
		labels = append(labels, resources[i]+":"+actions[i])
	}
	return labels
}

// revokeDerivedGrantsTx revokes every derived grant of a cause — one grant, or
// every grant at one installation's authority-root node — through the SAME
// revokeScopeApply the cause's own revocation runs, so each carries the scope
// revocation's event and audit record instead of disappearing silently.
//
// Runs inside the caller's narrowing: the derived rows and the cause go in one
// transaction. A revocation that removed the cause and lost the derived rows to
// a later failure is not a state this can reach.
func (s *Service) revokeDerivedGrantsTx(ctx context.Context, actorID, orgID, causeGrantID, causeScopeNodeID string) error {
	derived, err := s.installationStore().DerivedScopeGrants(ctx, orgID, causeGrantID, causeScopeNodeID)
	if err != nil {
		return err
	}
	for _, grant := range derived {
		if err := s.revokeScopeApply(ctx, actorID, &gen.RevokeScopeRequest{
			OrgId:       orgID,
			SubjectId:   grant.GetSubjectId(),
			SubjectKind: grant.GetSubjectKind(),
			ScopePath:   grant.GetScopePath(),
			RoleId:      grant.GetRoleId(),
		}); err != nil {
			return fmt.Errorf("revoke the module access carried by %s: %w", grant.GetDerivedFromGrantId(), err)
		}
	}
	return nil
}
