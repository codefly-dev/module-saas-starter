package adapters

// The conformance fixture: what the shared suite needs from the composing
// workspace, built through this host's own paths. Modules and solutions are
// delivered as the CLI delivers them (a signed carrier and the authority's
// signed document through CompositionService.ApplyManifest). A solution is
// installed through the fixture's declared installations, which the authority
// core performs; a module by each organization's administrator through
// accounts' own install path (consentModule), which the core reads. Only the
// cluster stands in (executiontest).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modulecontract "github.com/codefly-dev/core/contracts/module"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"accounts/internal/deliverytest"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"

	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
)

// conformanceFixtureName is the harness's fixture, in testdata: the dev-admin
// people with the roles, assignments and installations the suite needs.
const conformanceFixtureName = "host-interface-conformance"

// conformanceFixturePath is the absolute path of the harness's fixture.
func conformanceFixturePath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", conformanceFixtureName+".yaml"))
	require.NoError(t, err)
	return path
}

// wireScope is one work scope of the interface.
func wireScope(kind string, actions ...string) *authorityv1.WorkScopeV1 {
	return &authorityv1.WorkScopeV1{ResourceKind: kind, Actions: actions}
}

// conformanceSolution is one solution the harness delivers: its own kinds and
// the actions it serves on them, its roles, its routes and its bindings.
type conformanceSolution struct {
	installationRole string
	edges            []modulecontract.EdgeDependency
	alias            string
	generation       uint64
	ceilings         []*authorityv1.WorkScopeV1
	roles            map[string][]*authorityv1.WorkScopeV1
	methods          []*compositionv1.MethodPolicyV1
	bindings         []solutionhost.AuthorityBinding
}

// deliverSolution delivers s as the CLI does: core's manifest signed with the
// release key, carried by the solution's own delivery credential, and the
// authority's document bound to it by module, generation and digest.
func deliverSolution(t *testing.T, ctx context.Context, conn grpc.ClientConnInterface, signer *solutionhost.InMemorySigner, s conformanceSolution) {
	t.Helper()
	m := &solutionhost.ModuleManifest{Schema: solutionhost.SchemaModuleManifestV1, Binding: s.alias, Alias: s.alias, Kind: solutionhost.KindSolution, Generation: s.generation,
		Host: solutionhost.HostTarget{Coordinate: "conformance-host", Domain: "acme"}, Release: solutionhost.Release{Publisher: "conformance", Name: s.alias, Version: "0.1.0"},
		Workloads: []solutionhost.Workload{{Service: s.alias, Container: "app"}}, Authority: &solutionhost.Authority{Principal: s.alias, Bindings: s.bindings, InstallationRole: s.installationRole, EdgeDependencies: s.edges}}
	for _, ceiling := range s.ceilings {
		m.Authority.ResourceKinds = append(m.Authority.ResourceKinds, ceiling.GetResourceKind())
	}
	raw, err := solutionhost.SignedPayload(m)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	// A solution receives owner-selected scopes, so it declares owner-aware checking.
	document := &compositionv1.AuthorityManifestV1{Module: s.alias, Generation: s.generation, CoreManifestDigest: "sha256:" + hex.EncodeToString(sum[:]),
		ScopeCeilings: s.ceilings, Methods: s.methods, OwnerAware: true, InstallationRole: s.installationRole}
	for _, edge := range s.edges {
		document.EdgeDependencies = append(document.EdgeDependencies, &compositionv1.EdgeDependencyV1{Module: edge.Module, Presence: edge.Presence})
	}
	for name, scopes := range s.roles {
		document.Roles = append(document.Roles, &compositionv1.RoleV1{Name: name, Scopes: scopes})
	}
	doc, err := (proto.MarshalOptions{Deterministic: true}).Marshal(document)
	require.NoError(t, err)
	envelope, err := delivery.Sign(doc, signer.KeyID(), signer.Sign)
	require.NoError(t, err)
	_, err = compositionv1.NewCompositionServiceClient(conn).ApplyManifest(ctx, &compositionv1.ApplyManifestRequest{
		Carrier:   &compositionv1.SignedManifestV1{Document: raw, Envelope: deliverytest.Envelope(t, raw, signer.KeyID())},
		Authority: &compositionv1.SignedManifestV1{Document: doc, Envelope: envelope}, Credential: "delivery:" + s.alias,
	})
	require.NoError(t, err, "deliver solution %s", s.alias)
}

// conformanceModule is one module the harness composes: its own kinds, each
// with the actions it serves, and its bindings. A module checks resource
// owners, so it declares owner-aware checking.
type conformanceModule struct {
	releaseVersion   string
	installationRole string
	alias            string
	ceilings         []*authorityv1.WorkScopeV1
	bindings         []solutionhost.AuthorityBinding
	// generation is the delivery's generation; zero delivers the first.
	generation uint64
}

// deliverModule delivers m as the CLI does: core's manifest signed with the
// release key, carried by the module's own delivery credential, and the
// authority's document bound to it by module, generation and digest.
func deliverModule(t *testing.T, ctx context.Context, conn grpc.ClientConnInterface, signer *solutionhost.InMemorySigner, m conformanceModule) {
	t.Helper()
	generation := max(m.generation, 1)
	version := m.releaseVersion
	if version == "" {
		version = "0.1.0"
	}
	manifest := &solutionhost.ModuleManifest{Schema: solutionhost.SchemaModuleManifestV1, Binding: m.alias, Alias: m.alias, Kind: solutionhost.KindModule, Generation: generation,
		Host: solutionhost.HostTarget{Coordinate: "conformance-host", Domain: "acme"}, Release: solutionhost.Release{Publisher: "conformance", Name: m.alias, Version: version},
		Workloads: []solutionhost.Workload{{Service: m.alias, Container: "app"}}, Authority: &solutionhost.Authority{Principal: m.alias, Bindings: m.bindings, InstallationRole: m.installationRole}}
	for _, ceiling := range m.ceilings {
		manifest.Authority.ResourceKinds = append(manifest.Authority.ResourceKinds, ceiling.GetResourceKind())
	}
	raw, err := solutionhost.SignedPayload(manifest)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	doc, err := (proto.MarshalOptions{Deterministic: true}).Marshal(&compositionv1.AuthorityManifestV1{Module: m.alias, Generation: generation,
		CoreManifestDigest: "sha256:" + hex.EncodeToString(sum[:]), ScopeCeilings: m.ceilings, OwnerAware: true, InstallationRole: m.installationRole})
	require.NoError(t, err)
	envelope, err := delivery.Sign(doc, signer.KeyID(), signer.Sign)
	require.NoError(t, err)
	_, err = compositionv1.NewCompositionServiceClient(conn).ApplyManifest(ctx, &compositionv1.ApplyManifestRequest{
		Carrier:   &compositionv1.SignedManifestV1{Document: raw, Envelope: deliverytest.Envelope(t, raw, signer.KeyID())},
		Authority: &compositionv1.SignedManifestV1{Document: doc, Envelope: envelope}, Credential: "delivery:" + m.alias,
	})
	require.NoError(t, err, "deliver module %s", m.alias)
}

// teamNamed is the id of the organisation's team called name.
func teamNamed(t *testing.T, ctx context.Context, svc *business.Service, orgID, name string) string {
	t.Helper()
	var teams []*gen.Team
	require.NoError(t, svc.Store().WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		teams, err = svc.Store().ListTeams(ctx, orgID, "")
		return err
	}))
	for _, team := range teams {
		if team.GetName() == name {
			return team.GetId()
		}
	}
	t.Fatalf("organization %s has no team %q", orgID, name)
	return ""
}

// consentModule has org's administrator install a delivered module, or
// consent again to its current delivery when it is installed, through
// accounts' own path (the fixture's install: a role of the organization, an
// approval of the reviewed target and generation, and its install step
// completed on the durable queue). Nothing writes an installation directly;
// the authority core reads the one this makes.
func consentModule(ctx context.Context, svc *business.Service, store *infra.PostgresStore, org, admin, module string) error {
	// One role per module and organization: a consent given again keeps the
	// role the installation holds, which is immutable.
	role := "Use " + module
	if err := store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		return store.CreateRole(ctx, &gen.Role{Id: business.NewIDString(), OrgId: org, Name: role, Permissions: []*gen.Permission{{Resource: "solution", Action: "use"}}})
	}); err != nil && !strings.Contains(err.Error(), "roles_org_id_name_key") {
		return err
	}
	installation, _, err := svc.InstallFixtureTarget(ctx, admin, org, module, role, admin)
	if err != nil {
		return fmt.Errorf("install %s at %s: %w", module, org, err)
	}
	return completeInstallStep(ctx, store, installation)
}

// completeInstallStep completes the installation's step on the durable queue,
// as the install worker would, until the installation is active.
func completeInstallStep(ctx context.Context, store *infra.PostgresStore, installation *gen.Installation) error {
	if installation.GetStatus() != gen.InstallationStatus_INSTALLATION_STATUS_PENDING {
		return nil
	}
	pool, err := infra.NewJobWorkerPool(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	worker := infra.NewPostgresJobStore(pool)
	for range 1000 {
		batch, err := worker.Claim(ctx, &jobsv1.ClaimJobsRequest{Queue: business.InstallationStepQueue, WorkerId: "conformance-install", Limit: 1, LeaseDuration: durationpb.New(time.Minute)})
		if err != nil {
			return err
		}
		if len(batch.GetJobs()) != 1 {
			return fmt.Errorf("installation %s: no installation step is enqueued", installation.GetId())
		}
		job := batch.GetJobs()[0]
		if err := worker.Complete(ctx, &jobsv1.CompleteJobRequest{Lease: &jobsv1.JobLeaseReference{JobId: job.GetId(), WorkerId: job.GetLease().GetOwner(), LeaseToken: job.GetLease().GetToken()}}); err != nil {
			return err
		}
		if job.GetAttributes()["installation_step.id"] != installation.GetId() {
			continue
		}
		var current *gen.Installation
		if err := store.WithOrgTx(ctx, installation.GetOrgId(), func(ctx context.Context) error {
			var err error
			current, _, err = store.GetInstallation(ctx, installation.GetOrgId(), installation.GetId())
			return err
		}); err != nil {
			return err
		}
		if current.GetStatus() != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE {
			return fmt.Errorf("installation %s is %s after its step: %s", installation.GetId(), current.GetStatus(), current.GetFailureReason())
		}
		return nil
	}
	return fmt.Errorf("installation %s never completed its step", installation.GetId())
}
