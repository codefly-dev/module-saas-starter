package infra

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"accounts/pkg/business"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/jackc/pgx/v5"
)

// Keep the host's reviewed plan beside the canonical receipt, in the existing
// consent JSON. The core still reads exactly ConsentReceipt. There is no second
// solution installation or mirrored lifecycle to reconcile.
type solutionConsent struct {
	domain.ConsentReceipt
	Plan *business.SolutionInstallationPlan `json:"host_plan,omitempty"`
}

func (t coreTx) solutionConsentForWrite(inst domain.Installation) ([]byte, error) {
	var previous solutionConsent
	var raw []byte
	var previousStatus string
	err := t.q.QueryRow(t.ctx, `SELECT status, consent FROM public.authority_core_installations WHERE id=$1 FOR UPDATE`, inst.ID).Scan(&previousStatus, &raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(raw, &previous); err != nil {
			return nil, err
		}
	}
	current := solutionConsent{ConsentReceipt: inst.Consent}
	if previous.ConsentReceipt == inst.Consent {
		current.Plan = previous.Plan
	}
	if inst.Status == domain.Pending && current.Plan == nil {
		if plan, ok := business.SolutionInstallationPlanFromContext(t.ctx); ok {
			if plan.Organization != inst.Organization || plan.Target != inst.Target || plan.MachineRole != inst.AgentRole ||
				plan.Approval.OperationID != inst.Consent.OperationID || inst.Consent.OperationID == "" {
				return nil, domain.NotNow("INSTALLATION_PLAN_MISMATCH: the reviewed plan names another install attempt")
			}
			plan.InstallationID, plan.Revision, plan.Receipt = inst.ID, inst.Revision, inst.Consent
			current.Plan = &plan
		} else if t.hostSolutions {
			return nil, domain.NotNow("INSTALLATION_PLAN_REQUIRED: review the solution and its supporting modules before installing")
		}
	}
	if inst.Status == domain.Active && previousStatus == string(domain.Pending) && (t.hostSolutions || current.Plan != nil) {
		if current.Plan == nil {
			return nil, domain.NotNow("INSTALLATION_PLAN_REQUIRED: the pending installation has no reviewed plan")
		}
		if err := t.validateSolutionActivation(inst, *current.Plan); err != nil {
			return nil, err
		}
	}
	return json.Marshal(current)
}

// Every read below uses this SAME serializable core transaction. Locks protect
// the durable delivery and module consent, not an in-memory registry snapshot.
// No callback, enqueue, credential issuance or network effect is permitted here.
func (t coreTx) validateSolutionActivation(inst domain.Installation, plan business.SolutionInstallationPlan) error {
	var target, binding, release, digest, principal string
	var generation, coreGeneration uint64
	var declaration []byte
	err := t.q.QueryRow(t.ctx, `SELECT t.id::text,b.binding_id,b.applied_release,b.applied_digest,b.applied_generation,m.principal,m.generation,m.declarations
 FROM public.solution_targets t JOIN public.solution_host_bindings b ON b.binding_id=t.binding_id
 JOIN public.authority_core_modules m ON m.name=t.solution_id
 WHERE t.id=$1::uuid AND t.closed_generation IS NULL AND NOT b.applied_removed AND m.kind='solution'
 FOR SHARE OF t,b,m`, plan.HostTargetID).Scan(&target, &binding, &release, &digest, &generation, &principal, &coreGeneration, &declaration)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NotNow("INSTALLATION_DELIVERY_CHANGED: the reviewed solution is no longer delivered")
	}
	if err != nil {
		return err
	}
	if target != plan.HostTargetID || binding != plan.BindingID || release != plan.Release || digest != plan.DeliveryDigest ||
		generation != plan.Approval.Generation || coreGeneration != plan.Approval.Generation || principal != plan.Approval.TargetID {
		return domain.NotNow("INSTALLATION_DELIVERY_CHANGED: review the current solution release again")
	}
	module := domain.Module{Name: inst.Target, Principal: principal, Kind: domain.KindSolution, Generation: coreGeneration}
	if err := decodeModule(&module, declaration); err != nil {
		return err
	}
	principals := make([]string, 0, len(module.Bindings)+len(module.EdgeDependencies))
	for _, binding := range module.Bindings {
		if binding.Audience != principal {
			principals = append(principals, binding.Audience)
		}
	}
	for _, edge := range module.EdgeDependencies {
		principals = append(principals, edge.Module)
	}
	rows, err := t.q.Query(t.ctx, `SELECT `+coreModuleColumns+` FROM public.authority_core_modules WHERE principal=ANY($1) ORDER BY name FOR SHARE`, principals)
	if err != nil {
		return err
	}
	modules, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Module, error) { return scanCoreModule(row) })
	if err != nil {
		return err
	}
	if err := plan.ValidateDependencySet(authority.ReviewedOffer{Module: module, Modules: modules}); err != nil {
		return err
	}
	dependencies := slices.Clone(plan.Dependencies)
	slices.SortFunc(dependencies, func(a, b business.ModuleInstallationPrerequisite) int {
		return strings.Compare(a.InstallationID, b.InstallationID)
	})
	observed := make([]business.ModuleInstallationPrerequisite, 0, len(dependencies))
	for _, expected := range dependencies {
		if err := checkInstallationEnvelope(t.ctx, t.q, expected.EnvelopeRevision); err != nil {
			return err
		}
		current, err := t.modulePrerequisite(expected.InstallationID, inst.Organization, expected.EnvelopeRevision)
		if err != nil {
			return err
		}
		observed = append(observed, current)
	}
	job, err := t.acknowledgedSolutionSetup(inst, plan.SetupJobID)
	if err != nil {
		return err
	}
	return plan.ValidateActivation(inst, observed, job)
}

func (t coreTx) acknowledgedSolutionSetup(inst domain.Installation, jobID string) (string, error) {
	state, _, _, err := readCanonicalSetupJob(t.ctx, t.q, inst, jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil || state != "succeeded" {
		return "", err
	}
	return jobID, nil
}

func (t coreTx) modulePrerequisite(id, org string, envelope uint64) (business.ModuleInstallationPrerequisite, error) {
	var current business.ModuleInstallationPrerequisite
	var scope, receipt []byte
	err := t.q.QueryRow(t.ctx, `SELECT i.id::text,i.org_id::text,i.revision,i.target_id::text,i.registry_binding_id,i.registry_principal_id::text,
 b.applied_generation,b.applied_digest,i.scope,i.consent_receipt,i.no_standing_authority,
 COALESCE((SELECT g.role_id::text FROM public.scope_grants g JOIN public.scope_nodes n ON n.id=i.root_scope_node_id
   WHERE g.org_id=i.org_id AND g.subject_kind='principal' AND g.subject_id=i.agent_principal_id AND g.scope_path=n.scope_path
     AND (g.expires_at IS NULL OR g.expires_at>now()) FOR SHARE OF g,n),'')
 FROM public.installations i JOIN public.solution_targets t ON t.id=i.target_id
 JOIN public.solution_host_bindings b ON b.binding_id=t.binding_id
 WHERE i.id=$1::uuid AND i.org_id=$2::uuid AND i.kind='module' AND i.status='active'
 AND t.closed_generation IS NULL AND NOT b.applied_removed FOR SHARE OF i,t,b`, id, org).
		Scan(&current.InstallationID, &current.Organization, &current.Revision, &current.TargetID, &current.BindingID, &current.PrincipalID,
			&current.Generation, &current.DeliveryDigest, &scope, &receipt, &current.NoStandingAuthority, &current.RoleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return current, domain.NotNow("INSTALLATION_PREREQUISITES_CHANGED: a required module is absent or revoked")
	}
	if err != nil {
		return current, err
	}
	current.EnvelopeRevision = envelope // checked under the owner's envelope lock above
	var consentReceipt installationApprovalReceipt
	if err := json.Unmarshal(receipt, &consentReceipt); err != nil {
		return current, err
	}
	current.ConsentOperationID, current.ConsentIntent, current.ConsentRevision = consentReceipt.OperationID, consentReceipt.Fingerprint, consentReceipt.Revision
	var consent business.InstallationScope
	if err := json.Unmarshal(scope, &consent); err != nil {
		return current, err
	}
	normalized, err := json.Marshal(consent)
	if err != nil {
		return current, err
	}
	current.ScopeDigest = fmt.Sprintf("%x", sha256.Sum256(normalized))
	if !consent.Capabilities {
		return current, domain.NotNow("module consent grants no capability")
	}
	return current, nil
}
