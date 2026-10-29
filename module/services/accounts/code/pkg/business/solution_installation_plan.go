package business

import (
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"fmt"
	"slices"

	"google.golang.org/protobuf/proto"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
)

// SolutionInstallationPlan records reviewed inputs and prerequisite witnesses,
// not installation status. The shared core owns status, revision and receipt.
// Dependencies are populated only after the existing module owner has recorded
// or reused sufficient consent; the plan does not grant module authority.
type SolutionInstallationPlan struct {
	InstallationID string
	Organization   string
	Target         string
	MachineRole    string
	RequestedRole  string
	RequestedOwner string
	Revision       uint64
	Receipt        domain.ConsentReceipt
	Approval       domain.ConsentApproval
	HostTargetID   string
	BindingID      string
	Release        string
	DeliveryDigest string
	SetupJobID     string
	Dependencies   []ModuleInstallationPrerequisite
	Review         *gen.ReviewedSolutionInstall
}

type solutionPlanContextKey struct{}

// WithSolutionInstallationPlan is used by the host's reviewed-install entry
// after validating its complete review. Copy it so caller mutation cannot alter
// the plan carried into a retryable authority transaction.
func WithSolutionInstallationPlan(ctx context.Context, plan SolutionInstallationPlan) context.Context {
	plan.Dependencies = slices.Clone(plan.Dependencies)
	plan.Review = proto.CloneOf(plan.Review)
	return context.WithValue(ctx, solutionPlanContextKey{}, plan)
}

func SolutionInstallationPlanFromContext(ctx context.Context) (SolutionInstallationPlan, bool) {
	plan, ok := ctx.Value(solutionPlanContextKey{}).(SolutionInstallationPlan)
	return plan, ok
}

// ModuleInstallationPrerequisite is the exact module consent that satisfied a
// reviewed requirement. ScopeDigest identifies the recorded scope, not a new
// permission vocabulary. EnvelopeRevision also fences effective consent that
// follows an administrative envelope. The final SQL transaction reads these
// facts from locked installation and durable delivery rows, never a cache.
type ModuleInstallationPrerequisite struct {
	InstallationID      string
	Organization        string
	Revision            uint64
	TargetID            string
	BindingID           string
	PrincipalID         string
	Generation          uint64
	DeliveryDigest      string
	ScopeDigest         string
	RoleID              string
	NoStandingAuthority bool
	EnvelopeRevision    uint64
	ConsentOperationID  string
	ConsentIntent       string
	ConsentRevision     uint64
}

// ValidateActivation is the pure comparison used inside the canonical store's
// final serializable write. Reading and locking the observed rows is the store's
// responsibility. It must call this before writing active, and perform no
// enqueue or other external effect while Store.Write may retry.
func (p SolutionInstallationPlan) ValidateActivation(inst domain.Installation, observed []ModuleInstallationPrerequisite, acknowledgedJobID string) error {
	refuse := func(reason string) error {
		return domain.NotNow("INSTALLATION_PREREQUISITES_CHANGED: %s; review the installation again", reason)
	}
	if p.InstallationID == "" || p.HostTargetID == "" || p.BindingID == "" || p.Release == "" || p.DeliveryDigest == "" ||
		p.Approval.TargetID == "" || p.Approval.Generation == 0 || p.Approval.Digest == "" ||
		p.Receipt.OperationID == "" || p.Receipt.Intent == "" || p.Receipt.Revision == 0 ||
		p.Approval.OperationID != p.Receipt.OperationID {
		return refuse("the reviewed plan is incomplete")
	}
	if inst.ID != p.InstallationID || inst.Organization != p.Organization || inst.Target != p.Target ||
		inst.AgentRole != p.MachineRole || inst.Revision != p.Revision || inst.Revision != p.Receipt.Revision || inst.Consent != p.Receipt {
		return refuse("the canonical attempt no longer matches the reviewed plan")
	}
	if inst.Status != domain.Active {
		return refuse("the requested transition is not activation")
	}
	if p.SetupJobID == "" || acknowledgedJobID != p.SetupJobID {
		return refuse("the exact setup job has not acknowledged this attempt")
	}
	if len(observed) != len(p.Dependencies) {
		return refuse("a required module installation is absent or revoked")
	}
	byID := make(map[string]ModuleInstallationPrerequisite, len(observed))
	for _, current := range observed {
		if _, duplicate := byID[current.InstallationID]; duplicate {
			return refuse("a prerequisite was returned more than once")
		}
		byID[current.InstallationID] = current
	}
	seen := make(map[string]bool, len(p.Dependencies))
	for _, required := range p.Dependencies {
		if required.InstallationID == "" || required.Organization != inst.Organization || required.Revision == 0 ||
			required.TargetID == "" || required.BindingID == "" || required.PrincipalID == "" || required.Generation == 0 ||
			required.DeliveryDigest == "" || required.ScopeDigest == "" ||
			(required.NoStandingAuthority && required.RoleID != "" || !required.NoStandingAuthority && required.RoleID == "") || seen[required.InstallationID] {
			return refuse("the module consent witness is incomplete or duplicated")
		}
		seen[required.InstallationID] = true
		if current, exists := byID[required.InstallationID]; !exists || current != required {
			return refuse(fmt.Sprintf("module installation %s changed after review", required.InstallationID))
		}
	}
	return nil
}
