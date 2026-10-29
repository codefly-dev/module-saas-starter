package business

import (
	"context"
	"encoding/json"
	"fmt"

	installationv1 "accounts/pkg/gen/host/installation/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

type solutionBundleStore interface {
	CanonicalInstallationSetupStore
	ModuleInstallationPrerequisite(context.Context, string, string, uint64) (ModuleInstallationPrerequisite, error)
	RecordSolutionPrerequisites(context.Context, domain.Installation, []ModuleInstallationPrerequisite) error
}

// ReviewSolutionInstall uses the shared solution offer and the existing host
// catalogue/module consent owner. It creates no installation, grant or job.
func (s *Service) ReviewSolutionInstall(ctx context.Context, caller authority.Person, org, targetID string) (*gen.ReviewedSolutionInstall, error) {
	return s.reviewSolutionInstall(ctx, caller, org, targetID, NewIDString(), "")
}

func (s *Service) reviewSolutionInstall(ctx context.Context, caller authority.Person, org, targetID, operation, requestedRole string) (*gen.ReviewedSolutionInstall, error) {
	if uuid.Validate(operation) != nil {
		return nil, domain.Malformed("installation operation must be a UUID")
	}
	var targets []*AvailableSolutionTarget
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		targets, err = s.store.ListAvailableSolutionTargets(ctx, AvailableSolutionQuery{OrgID: org, TargetID: targetID, Limit: 1})
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(targets) != 1 || targets[0].Kind != "solution" {
		return nil, domain.NotNow("the reviewed solution target is not available")
	}
	core, err := s.AuthorityCore()
	if err != nil {
		return nil, err
	}
	offer, err := core.ReviewOffer(ctx, caller, org, targets[0].RouteAlias)
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
		return nil, domain.NotNow("solution %q declares no installation authority; its owner must declare it before installation", offer.Module.Principal)
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
			return nil, domain.NotNow("module %q delivery changed; review again", dependency.Principal)
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
				return nil, domain.NotNow("module %q has insufficient current consent; review its access before installing this solution", dependency.Principal)
			}
			entry.ReuseInstallationId, entry.Approval.ExpectedRevision, scope = inst.Id, state.Revision, state.Scope
		} else if entry.InstallationRole != domain.NoInstallationRole {
			return nil, domain.NotNow("module %q cannot be installed automatically: its installation authority is absent or unsupported", dependency.Principal)
		}
		raw, err := json.Marshal(scope)
		if err != nil {
			return nil, err
		}
		entry.Scope = string(raw)
	}
	return review, nil
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

// InstallReviewedSolution preserves the entire original review on the canonical
// pending row before any module work. Core authorizes each continuation, while
// the existing module installer records each separate module consent.
func (s *Service) InstallReviewedSolution(ctx context.Context, caller authority.Person, org string, review *gen.ReviewedSolutionInstall) (domain.Installation, error) {
	return s.installReviewedSolution(ctx, caller, org, review, "", "")
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
		if inst.Status == domain.Pending && inst.Consent.OperationID != review.Approval.OperationId {
			continue // A fresh review must pass reconstruction and core's revision fence below.
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
	ctx = WithSolutionInstallationPlan(ctx, plan)
	return core.Install(ctx, caller, org, review.Target, requestedRole, requestedOwner, &plan.Approval, s.AuthorityInstallStep())
}

func (s *Service) convergeReviewedModules(ctx context.Context, inst domain.Installation, store solutionBundleStore) error {
	plan, err := store.SolutionInstallationPlan(ctx, inst.Organization, inst.ID)
	if err != nil || plan.Review == nil {
		return err
	}
	var witnesses []ModuleInstallationPrerequisite
	for _, reviewed := range plan.Review.Modules {
		if !reviewed.Required {
			continue
		}
		target, err := s.ResolveInstallationPrincipal(ctx, inst.Organization, reviewed.Principal)
		if err != nil {
			return err
		}
		_, digest, err := s.reviewedTargetDelivery(ctx, target)
		if err != nil {
			return err
		}
		if target.TargetID != reviewed.TargetId || target.BindingID != reviewed.BindingId || target.AppliedGeneration != reviewed.Approval.Generation || digest != reviewed.DeliveryDigest {
			return domain.NotNow("module %q changed since review", reviewed.Principal)
		}
		id := reviewed.ReuseInstallationId
		if id == "" {
			scope, err := ParseInstallationScope(reviewed.Scope)
			if err != nil {
				return err
			}
			a := reviewed.Approval
			installed, err := s.InstallSolution(ctx, plan.Review.ActorId, &InstallSolutionParams{OrgID: inst.Organization, TargetID: target.TargetID,
				AgentIdentifier: target.ReleasePublisher + "/" + target.ReleaseName + ":" + target.ReleaseVersion, Scope: &scope,
				Approval: &InstallationApproval{TargetID: a.TargetId, Generation: a.Generation, ExpectedRevision: a.ExpectedRevision, OperationID: a.OperationId}})
			if err != nil {
				return err
			}
			id = installed.Id
		}
		witness, err := store.ModuleInstallationPrerequisite(ctx, inst.Organization, id, s.moduleRegistrySnapshot().envelopeRevision)
		if err != nil {
			return err
		}
		if witness.TargetID != reviewed.TargetId || witness.DeliveryDigest != reviewed.DeliveryDigest {
			return domain.NotNow("module %q consent no longer matches review", reviewed.Principal)
		}
		if reviewed.ReuseInstallationId != "" && witness.Revision != reviewed.Approval.ExpectedRevision {
			return domain.NotNow("module %q consent changed after review", reviewed.Principal)
		}
		witnesses = append(witnesses, witness)
	}
	if err := store.RecordSolutionPrerequisites(ctx, inst, witnesses); err != nil {
		return fmt.Errorf("record reviewed module receipts: %w", err)
	}
	return nil
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
