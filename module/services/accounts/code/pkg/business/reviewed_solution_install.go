package business

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	installationv1 "accounts/pkg/gen/host/installation/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

type solutionBundleStore interface {
	CanonicalInstallationSetupStore
	ModuleInstallationPrerequisite(context.Context, string, string, uint64) (ModuleInstallationPrerequisite, error)
	RecordSolutionPrerequisites(context.Context, domain.Installation, []ModuleInstallationPrerequisite) error
}

// ReviewSolutionInstall uses the shared offer and the existing host
// catalogue/module consent owner. It creates no installation, grant or job.
// Its target is any role-gated target the core offers: a solution, or a
// module page declaring roles, which is reviewed, installed, converged and set
// up exactly as a solution is; the core refuses a module without roles, which
// is installed by a closure and never chosen.
func (s *Service) ReviewSolutionInstall(ctx context.Context, caller authority.Person, org, targetID string) (*gen.ReviewedSolutionInstall, error) {
	return s.reviewSolutionInstall(ctx, caller, org, targetID, NewIDString(), "")
}

// ReviewSolutionInstallAs is ReviewSolutionInstall for a caller that selects
// the target's installation role where its declaration selects none (a
// declaration that names one is never overridden); InstallReviewedSolutionAs
// installs that review with the same role. The workspace fixture installs its
// declared rows this way.
func (s *Service) ReviewSolutionInstallAs(ctx context.Context, caller authority.Person, org, targetID, role string) (*gen.ReviewedSolutionInstall, error) {
	return s.reviewSolutionInstall(ctx, caller, org, targetID, NewIDString(), role)
}

func (s *Service) reviewSolutionInstall(ctx context.Context, caller authority.Person, org, targetID, operation, requestedRole string) (*gen.ReviewedSolutionInstall, error) {
	if uuid.Validate(operation) != nil {
		return nil, domain.Malformed("installation operation must be a UUID")
	}
	available, err := s.availableTarget(ctx, org, targetID)
	if err != nil {
		return nil, err
	}
	core, err := s.AuthorityCore()
	if err != nil {
		return nil, err
	}
	offer, err := core.ReviewOffer(ctx, caller, org, available.RouteAlias)
	if err != nil {
		return nil, err
	}
	if offer.Approval.TargetID == "" {
		return nil, domain.Refuse("an organization administrator must approve installation")
	}
	if offer.Module.InstallationRole == "" {
		offer.Module.InstallationRole = requestedRole
	}
	if offer.Module.InstallationRole == "" {
		return nil, domain.NotNow("%q declares no installation authority; its owner must declare it before installation", offer.Module.Principal)
	}
	target, err := s.ResolveInstallationPrincipal(ctx, org, offer.Module.Principal)
	if err != nil {
		return nil, err
	}
	if target.TargetID != targetID || target.AppliedGeneration != offer.Approval.Generation {
		return nil, domain.NotNow("solution delivery changed; review again")
	}
	release, digest, err := s.reviewedTargetDelivery(ctx, target)
	if err != nil {
		return nil, err
	}
	a := offer.Approval
	review := &gen.ReviewedSolutionInstall{OrgId: org, ActorId: caller.ID, TargetId: targetID, Target: target.RouteAlias,
		BindingId: target.BindingID, Release: release, DeliveryDigest: digest, InstallationRole: offer.Module.InstallationRole,
		Approval: &installationv1.ConsentApproval{TargetId: a.TargetID, Generation: a.Generation, ExpectedRevision: a.ExpectedRevision, Digest: a.Digest, OperationId: operation}}
	dependencies, err := SolutionInstallationDependencies(offer)
	if err != nil {
		return nil, err
	}
	if disagreement := closureDisagreement(offer, dependencies); disagreement != "" {
		// The core answers the closure too (ReviewedOffer.Closure); the host's
		// walker follows the same rule (a role-gated target reached is chosen
		// by its own review and never walked) and adds what the review shows,
		// so a disagreement is a defect, said, never silently resolved.
		wool.Get(ctx).In("ReviewSolutionInstall").Warn("the core's closure and the host's reviewed dependencies disagree", wool.Field("target", offer.Module.Name), wool.Field("disagreement", disagreement))
	}
	for _, dependency := range dependencies {
		entry := &gen.ReviewedSupportingModule{Principal: dependency.Principal, Required: dependency.Required}
		review.Modules = append(review.Modules, entry)
		if !dependency.Required {
			continue
		} // Optional presence never creates consent.
		target, err := s.ResolveInstallationPrincipal(ctx, org, dependency.Principal)
		if err != nil {
			return nil, err
		}
		if target.Kind != "module" || target.AppliedGeneration != dependency.Module.Generation {
			return nil, domain.NotNow("%s delivery changed; review again", dependency.Describe())
		}
		entry.TargetId, entry.Target, entry.BindingId = target.TargetID, target.RouteAlias, target.BindingID
		entry.Release, entry.DeliveryDigest, err = s.reviewedTargetDelivery(ctx, target)
		if err != nil {
			return nil, err
		}
		entry.InstallationRole = dependency.Module.InstallationRole
		entry.Approval = &installationv1.ConsentApproval{TargetId: target.TargetID, Generation: target.AppliedGeneration,
			OperationId: uuid.NewSHA1(uuid.MustParse(operation), []byte(dependency.Principal)).String()}
		snapshot := s.moduleRegistrySnapshot()
		_, grant, err := installationRegistryEntry(snapshot, target.BindingID, org)
		if err != nil {
			return nil, err
		}
		scope := FullInstallationScope(grant)
		if target.Installed {
			inst, err := s.heldTargetInstallation(ctx, org, target.TargetID)
			if err != nil {
				return nil, err
			}
			var state *InstallationAuthorityState
			err = s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
				var err error
				state, err = s.store.(InstallationScopeStore).InstallationAuthorityState(ctx, org, inst.Id)
				return err
			})
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(state.Scope)
			if err != nil {
				return nil, err
			}
			_, active, err := s.moduleConsent(dependency.Principal, raw, snapshot.envelopeRevision)
			if err != nil {
				return nil, err
			}
			if !active || inst.Status != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE {
				return nil, domain.NotNow("%s has insufficient current consent; review its access before installing this solution", dependency.Describe())
			}
			entry.ReuseInstallationId, entry.Approval.ExpectedRevision, scope = inst.Id, state.Revision, state.Scope
		} else if entry.InstallationRole = supportingInstallationRole(dependency.Module); entry.InstallationRole != domain.NoInstallationRole {
			return nil, domain.NotNow("%s cannot be installed automatically: its installation authority is absent or unsupported", dependency.Describe())
		} else {
			// Revoked history still owns the revision a fresh consent must name.
			for token := ""; ; {
				page, err := s.ListInstallations(ctx, &gen.ListInstallationsRequest{OrgId: org, PageToken: token, PageSize: listInstallationsMaxPageSize})
				if err != nil {
					return nil, err
				}
				for _, row := range page.Installations {
					if inst := row.GetInstallation(); inst.GetTargetId() == target.TargetID {
						entry.Approval.ExpectedRevision = max(entry.Approval.ExpectedRevision, inst.GetRevision())
					}
				}
				if token = page.NextPageToken; token == "" {
					break
				}
			}
		}
		raw, err := json.Marshal(scope)
		if err != nil {
			return nil, err
		}
		entry.Scope = string(raw)
	}
	return review, nil
}

// availableTarget is targetID as this host offers it to org; one it does not
// offer is not available to review.
func (s *Service) availableTarget(ctx context.Context, org, targetID string) (*AvailableSolutionTarget, error) {
	var targets []*AvailableSolutionTarget
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		targets, err = s.store.ListAvailableSolutionTargets(ctx, AvailableSolutionQuery{OrgID: org, TargetID: targetID, Limit: 1})
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(targets) != 1 {
		return nil, domain.NotNow("the reviewed target is not available")
	}
	return targets[0], nil
}

func (s *Service) reviewedTargetDelivery(ctx context.Context, target *AvailableSolutionTarget) (release, digest string, err error) {
	err = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		record, err := s.store.GetSolutionHostBindingForUpdate(ctx, target.BindingID)
		if err != nil {
			return err
		}
		if record == nil || record.Applied == nil || record.Applied.Removed || record.Applied.Generation != target.AppliedGeneration {
			return domain.NotNow("delivery changed for target %q; review again", target.TargetID)
		}
		release, digest = record.Applied.Release, record.Applied.Digest
		return nil
	})
	return
}

// ReviewCurrent reports whether review still names what is delivered: the
// target composed by the core and applied by this host at the generation it
// was reviewed at, with the delivery digest it reviewed, and every required
// supporting module at the generation and digest it reviewed. Replaying a
// review that is not current is refused every time
// (INSTALLATION_APPROVAL_GENERATION_CHANGED, or a module changed since
// review), so a pending attempt is resumed from its review only while it is
// current and reviewed afresh otherwise. A read that fails answers false: the
// fresh review then says why.
func (s *Service) ReviewCurrent(ctx context.Context, review *gen.ReviewedSolutionInstall) bool {
	core, err := s.AuthorityCore()
	if err != nil {
		return false
	}
	if m, err := core.Composed(ctx, review.GetTarget()); err != nil || m.Generation != review.GetApproval().GetGeneration() {
		return false
	}
	target, err := s.availableTarget(ctx, review.GetOrgId(), review.GetTargetId())
	if err != nil || target.BindingID != review.GetBindingId() || target.AppliedGeneration != review.GetApproval().GetGeneration() {
		return false
	}
	if _, digest, err := s.reviewedTargetDelivery(ctx, target); err != nil || digest != review.GetDeliveryDigest() {
		return false
	}
	for _, reviewed := range review.GetModules() {
		if !reviewed.GetRequired() {
			continue
		}
		if _, err := s.reviewedModuleTarget(ctx, review.GetOrgId(), reviewed); err != nil {
			return false
		}
	}
	return true
}

// InstallReviewedSolution preserves the entire original review on the canonical
// pending row before any module work. Core authorizes each continuation, while
// the existing module installer records each separate module consent.
func (s *Service) InstallReviewedSolution(ctx context.Context, caller authority.Person, org string, review *gen.ReviewedSolutionInstall) (domain.Installation, error) {
	return s.installReviewedSolution(ctx, caller, org, review, "", "")
}

// InstallReviewedSolutionAs installs a review made by ReviewSolutionInstallAs
// with the role it was made with; resuming it names the same role.
func (s *Service) InstallReviewedSolutionAs(ctx context.Context, caller authority.Person, org string, review *gen.ReviewedSolutionInstall, role string) (domain.Installation, error) {
	return s.installReviewedSolution(ctx, caller, org, review, role, "")
}

func (s *Service) installReviewedSolution(ctx context.Context, caller authority.Person, org string, review *gen.ReviewedSolutionInstall, requestedRole, requestedOwner string) (domain.Installation, error) {
	if review == nil || review.OrgId != org || review.ActorId != caller.ID || review.Approval == nil {
		return domain.Installation{}, domain.Refuse("the installation review belongs to another actor or organization")
	}
	core, err := s.AuthorityCore()
	if err != nil {
		return domain.Installation{}, err
	}
	all, err := core.Installations(ctx, caller, org)
	if err != nil {
		return domain.Installation{}, err
	}
	for _, inst := range all {
		if inst.Target != review.Target || (inst.Status != domain.Pending && inst.Status != domain.Active) {
			continue
		}
		if inst.Consent.OperationID != review.Approval.OperationId {
			// A fresh review must pass reconstruction and core's revision
			// fence below: on a pending attempt it supersedes it; on an
			// active installation it is an upgrade to the delivered offer,
			// or refused by the core as already installed when it approves
			// the offer the installation already holds.
			continue
		}
		plan, err := s.store.(CanonicalInstallationSetupStore).SolutionInstallationPlan(ctx, org, inst.ID)
		if err != nil {
			return inst, err
		}
		if !proto.Equal(plan.Review, review) || plan.RequestedRole != requestedRole || plan.RequestedOwner != requestedOwner {
			return inst, domain.NotNow("pending installation has a different original review")
		}
		return s.continueCanonicalSolution(ctx, core, caller, inst, plan)
	}
	current, err := s.reviewSolutionInstall(ctx, caller, org, review.TargetId, review.Approval.OperationId, requestedRole)
	if err != nil {
		return domain.Installation{}, err
	}
	if !proto.Equal(current, review) {
		return domain.Installation{}, domain.NotNow("installation review changed; review the solution and supporting access again")
	}
	a := review.Approval
	plan := SolutionInstallationPlan{Organization: org, Target: review.Target, MachineRole: review.InstallationRole, RequestedRole: requestedRole, RequestedOwner: requestedOwner,
		HostTargetID: review.TargetId, BindingID: review.BindingId, Release: review.Release, DeliveryDigest: review.DeliveryDigest,
		Review: review, Approval: domain.ConsentApproval{TargetID: a.TargetId,
			Generation: a.Generation, ExpectedRevision: a.ExpectedRevision, Digest: a.Digest, OperationID: a.OperationId}}
	// Check first: a supporting module that would be refused refuses the
	// whole install here, before the core records a pending attempt and
	// before any module is installed.
	if err := s.checkReviewedModulesInstallable(ctx, org, review); err != nil {
		return domain.Installation{}, err
	}
	ctx = WithSolutionInstallationPlan(ctx, plan)
	step, err := s.installStepFor(ctx, core, review.Target)
	if err != nil {
		return domain.Installation{}, err
	}
	return core.Install(ctx, caller, org, review.Target, requestedRole, requestedOwner, &plan.Approval, step)
}

// installStepFor is the install step the core runs for target: one that
// declares an install endpoint (DeclaresInstallStep) converges its closure and
// runs its setup through the durable queue (AuthorityInstallStep); one that
// declares none, a module page, converges its closure and nothing more, and
// the installation is active as soon as the core records it.
func (s *Service) installStepFor(ctx context.Context, core *authority.Service, target string) (authority.InstallStep, error) {
	m, err := core.Composed(ctx, target)
	if err != nil {
		return nil, err
	}
	if DeclaresInstallStep(m) {
		return s.AuthorityInstallStep(), nil
	}
	return s.convergeClosureStep, nil
}

// convergeClosureStep is the whole install step of a target without an
// install endpoint: every supporting module its review reached is installed or
// reused, and the witnesses are recorded on the plan for the activation fence.
func (s *Service) convergeClosureStep(ctx context.Context, inst domain.Installation) error {
	_, err := s.ConvergeReviewedClosure(ctx, inst)
	return err
}

// ConvergeReviewedClosure installs or reuses every supporting module the
// reviewed plan of inst reached, and records the witnesses the activation
// fence compares. It reports whether inst is an upgrade in flight: the plan
// then is the one reviewed for this attempt, carried by the caller, and its
// witnesses ride on it for the core's write, since the row still holds the
// consent that stands until the core moves it.
func (s *Service) ConvergeReviewedClosure(ctx context.Context, inst domain.Installation) (upgrade bool, err error) {
	bundle, ok := s.store.(solutionBundleStore)
	if !ok {
		return false, domain.NotNow("INSTALLATION_SETUP_UNAVAILABLE: the host cannot converge supporting modules")
	}
	return s.convergeReviewedModules(ctx, inst, bundle)
}

// reviewedPlanFor is the plan the install step of inst converges: the one the
// row records (a fresh install, or its retry), or, when the row's consent is
// another operation than inst's, the one reviewed for the upgrade in flight,
// carried in the context by installReviewedSolution.
func (s *Service) reviewedPlanFor(ctx context.Context, inst domain.Installation, store solutionBundleStore) (plan SolutionInstallationPlan, upgrade bool, err error) {
	carried := solutionInstallationPlanRef(ctx)
	if carried != nil && carried.Organization == inst.Organization && carried.Target == inst.Target && carried.Approval.OperationID == inst.Consent.OperationID {
		// The attempt in flight: an upgrade when the row's consent is another
		// operation's, which the core moves once this step converges.
		recorded, err := store.SolutionInstallationPlan(ctx, inst.Organization, inst.ID)
		switch {
		case err == nil && recorded.Approval.OperationID == inst.Consent.OperationID:
			return recorded, false, nil
		case err == nil:
			return *carried, true, nil
		}
		if code, ok := domain.CodeOf(err); ok && code == domain.NotFound {
			return *carried, true, nil
		}
		return plan, false, err
	}
	recorded, err := store.SolutionInstallationPlan(ctx, inst.Organization, inst.ID)
	if err != nil {
		return plan, false, err
	}
	if recorded.Approval.OperationID != inst.Consent.OperationID {
		return plan, false, domain.NotNow("INSTALLATION_PLAN_CHANGED: the attempt in flight has no reviewed plan for operation %s", inst.Consent.OperationID)
	}
	return recorded, false, nil
}

func (s *Service) convergeReviewedModules(ctx context.Context, inst domain.Installation, store solutionBundleStore) (bool, error) {
	plan, upgrade, err := s.reviewedPlanFor(ctx, inst, store)
	if err != nil || plan.Review == nil {
		return upgrade, err
	}
	// A resumed attempt checks first too: what an earlier attempt installed
	// is installed again idempotently below (the same operation replays its
	// receipt), and nothing is installed when any entry would be refused.
	if err := s.checkReviewedModulesInstallable(ctx, inst.Organization, plan.Review); err != nil {
		return upgrade, err
	}
	var witnesses []ModuleInstallationPrerequisite
	for _, reviewed := range plan.Review.Modules {
		if !reviewed.Required {
			continue
		}
		target, err := s.reviewedModuleTarget(ctx, inst.Organization, reviewed)
		if err != nil {
			return upgrade, err
		}
		id := reviewed.ReuseInstallationId
		if id == "" {
			params, err := reviewedModuleInstall(inst.Organization, target, reviewed)
			if err != nil {
				return upgrade, err
			}
			installed, err := s.InstallSolution(ctx, plan.Review.ActorId, params)
			if err != nil {
				return upgrade, err
			}
			id = installed.Id
		}
		witness, err := store.ModuleInstallationPrerequisite(ctx, inst.Organization, id, s.moduleRegistrySnapshot().envelopeRevision)
		if err != nil {
			return upgrade, err
		}
		if witness.TargetID != reviewed.TargetId || witness.DeliveryDigest != reviewed.DeliveryDigest {
			return upgrade, domain.NotNow("module %q consent no longer matches review", reviewed.Principal)
		}
		if reviewed.ReuseInstallationId != "" && witness.Revision != reviewed.Approval.ExpectedRevision {
			return upgrade, domain.NotNow("module %q consent changed after review", reviewed.Principal)
		}
		witnesses = append(witnesses, witness)
	}
	if upgrade {
		// No pending row to record them on: the core's write of the upgrade
		// persists the carried plan, witnesses included.
		solutionInstallationPlanRef(ctx).Dependencies = witnesses
		return true, nil
	}
	if err := store.RecordSolutionPrerequisites(ctx, inst, witnesses); err != nil {
		return false, fmt.Errorf("record reviewed module receipts: %w", err)
	}
	return false, nil
}

// checkReviewedModulesInstallable holds every supporting module review
// installs fresh to the predicates InstallSolution holds it to
// (checkInstallable), and writes nothing: the first refusal refuses the whole
// install before any module is installed. A module the review reuses is
// installed already and is fenced by its witness instead.
func (s *Service) checkReviewedModulesInstallable(ctx context.Context, org string, review *gen.ReviewedSolutionInstall) error {
	for _, reviewed := range review.GetModules() {
		if !reviewed.Required || reviewed.ReuseInstallationId != "" {
			continue
		}
		target, err := s.reviewedModuleTarget(ctx, org, reviewed)
		if err != nil {
			return err
		}
		params, err := reviewedModuleInstall(org, target, reviewed)
		if err != nil {
			return err
		}
		if err := s.checkInstallable(ctx, review.ActorId, params); err != nil {
			return err
		}
	}
	return nil
}

// reviewedModuleTarget is the host target a reviewed supporting module names,
// refused when its delivery moved since the review.
func (s *Service) reviewedModuleTarget(ctx context.Context, org string, reviewed *gen.ReviewedSupportingModule) (*AvailableSolutionTarget, error) {
	target, err := s.ResolveInstallationPrincipal(ctx, org, reviewed.Principal)
	if err != nil {
		return nil, err
	}
	_, digest, err := s.reviewedTargetDelivery(ctx, target)
	if err != nil {
		return nil, err
	}
	if target.TargetID != reviewed.TargetId || target.BindingID != reviewed.BindingId || target.AppliedGeneration != reviewed.GetApproval().GetGeneration() || digest != reviewed.DeliveryDigest {
		return nil, domain.NotNow("module %q changed since review", reviewed.Principal)
	}
	return target, nil
}

// reviewedModuleInstall is the install of one reviewed supporting module:
// its target, the scope and the approval the review carries.
func reviewedModuleInstall(org string, target *AvailableSolutionTarget, reviewed *gen.ReviewedSupportingModule) (*InstallSolutionParams, error) {
	scope, err := ParseInstallationScope(reviewed.Scope)
	if err != nil {
		return nil, err
	}
	a := reviewed.Approval
	return &InstallSolutionParams{OrgID: org, TargetID: target.TargetID,
		AgentIdentifier: target.ReleasePublisher + "/" + target.ReleaseName + ":" + target.ReleaseVersion, Scope: &scope,
		Approval: &InstallationApproval{TargetID: a.TargetId, Generation: a.Generation, ExpectedRevision: a.ExpectedRevision, OperationID: a.OperationId}}, nil
}

// InstallAuthoritySolution adapts the existing single-target consent. It may
// reuse supporting consent, but cannot approve additional module installations.
func (s *Service) InstallAuthoritySolution(ctx context.Context, caller authority.Person, org, target, role, owner string, approval *domain.ConsentApproval) (domain.Installation, error) {
	core, err := s.AuthorityCore()
	if err != nil {
		return domain.Installation{}, err
	}
	if approval == nil {
		return domain.Installation{}, domain.NotNow("INSTALLATION_APPROVAL_REQUIRED: review the solution before installation")
	}
	installed, err := core.Installations(ctx, caller, org)
	if err != nil {
		return domain.Installation{}, err
	}
	for _, inst := range installed {
		if inst.Target != target || inst.Consent.OperationID != approval.OperationID || (inst.Status != domain.Active && inst.Status != domain.Pending) {
			continue
		}
		plan, err := s.store.(CanonicalInstallationSetupStore).SolutionInstallationPlan(ctx, org, inst.ID)
		if err != nil {
			return inst, err
		}
		if plan.Approval != *approval || plan.RequestedRole != role || plan.RequestedOwner != owner {
			return inst, domain.NotNow("INSTALLATION_APPROVAL_OPERATION_CONFLICT: the original installation inputs changed")
		}
		return s.continueCanonicalSolution(ctx, core, caller, inst, plan)
	}
	module, err := core.Composed(ctx, target)
	if err != nil {
		return domain.Installation{}, err
	}
	resolved, err := s.ResolveInstallationPrincipal(ctx, org, module.Principal)
	if err != nil {
		return domain.Installation{}, err
	}
	review, err := s.reviewSolutionInstall(ctx, caller, org, resolved.TargetID, approval.OperationID, role)
	if err != nil {
		return domain.Installation{}, err
	}
	a := review.Approval
	switch {
	case a.TargetId != approval.TargetID || a.Generation != approval.Generation:
		return domain.Installation{}, domain.NotNow("INSTALLATION_APPROVAL_GENERATION_CHANGED: review the current delivery again")
	case a.Digest != approval.Digest:
		return domain.Installation{}, domain.NotNow("INSTALLATION_APPROVAL_DIGEST_CHANGED: review the current authority again")
	case a.ExpectedRevision != approval.ExpectedRevision:
		return domain.Installation{}, domain.NotNow("INSTALLATION_APPROVAL_REVISION_CHANGED: review the current installation again")
	}
	for _, dependency := range review.Modules {
		if dependency.Required && dependency.ReuseInstallationId == "" {
			return domain.Installation{}, domain.NotNow("module %q requires the combined installation review", dependency.Principal)
		}
	}
	return s.installReviewedSolution(ctx, caller, org, review, role, owner)
}

// closureDisagreement compares the modules the core's closure names with the
// required, delivered dependencies the host's walker found, by name; empty
// when they agree. The core lists what installing the target brings along;
// the host's walker lists what the review consents to, so the two must name
// the same supporting modules.
func closureDisagreement(offer authority.ReviewedOffer, dependencies []SolutionDependency) string {
	names := map[string]string{}
	for _, m := range offer.Modules {
		names[m.Principal] = m.Name
	}
	var walked []string
	for _, d := range dependencies {
		if d.Required && d.Delivered {
			walked = append(walked, names[d.Principal])
		}
	}
	slices.Sort(walked)
	core := slices.Clone(offer.Closure)
	slices.Sort(core)
	if slices.Equal(walked, core) {
		return ""
	}
	return fmt.Sprintf("the core's closure is %v, the host's walker found %v", core, walked)
}

// supportingInstallationRole is the standing role a required supporting
// module is installed holding; its delivered declaration decides. "none"
// declares no standing machine authority and a named role is its own. A
// manifest that declares neither an installation role nor any role at all
// (one written before the declaration existed) leaves this host nothing to
// select, so the module is installed holding none: all a solution's closure
// asks of it is to be installed, because the authority core issues a token
// toward a module in an organization only where that module has an
// installation. A module that declares roles is a module page, chosen by its
// own review, and never a supporting module (SolutionInstallationDependencies
// skips it). A reused installation keeps whatever it holds,
// so the entry carries the declaration as written and this is read only for
// a fresh install. The store holds the same rule under its delivery locks
// (lock_module_no_standing_declaration, migration 67).
func supportingInstallationRole(m domain.Module) string {
	if undeclaredAuthorityIsNone(m) {
		return domain.NoInstallationRole
	}
	return m.InstallationRole
}

// undeclaredAuthorityIsNone says whether m declares no installation role and
// no role at all, so that no standing authority exists for a host to select.
func undeclaredAuthorityIsNone(m domain.Module) bool {
	return m.Kind == domain.KindModule && m.InstallationRole == "" && len(m.Roles) == 0
}
