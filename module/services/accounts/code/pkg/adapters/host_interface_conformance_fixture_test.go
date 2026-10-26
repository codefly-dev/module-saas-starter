package adapters

// The conformance fixture: what the shared suite needs from the composing
// workspace, built through this host's own paths. Solutions are delivered as
// the CLI delivers them (a signed carrier and the authority's signed document
// through CompositionService.ApplyManifest), installed through the fixture's
// declared installations (InstallTargetByName, then team exposure), and
// acknowledged by the host's own install-step claim and finish, as a gateway
// replica does once the solution answers. Only external sources stand in: the
// cluster (executiontest), the module authority of a composition
// (compositiontest) and the append-only policy log.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"accounts/internal/deliverytest"
	"accounts/pkg/business"
	"accounts/pkg/business/compositiontest"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	installationv1 "accounts/pkg/gen/host/installation/v1"
	sessionv1 "accounts/pkg/gen/host/session/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

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
	alias      string
	generation uint64
	ceilings   []*authorityv1.WorkScopeV1
	roles      map[string][]*authorityv1.WorkScopeV1
	methods    []*compositionv1.MethodPolicyV1
	bindings   []solutionhost.AuthorityBinding
}

// deliverSolution delivers s as the CLI does: core's manifest signed with the
// release key, carried by the solution's own delivery credential, and the
// authority's document bound to it by module, generation and digest.
func deliverSolution(t *testing.T, ctx context.Context, conn grpc.ClientConnInterface, signer *solutionhost.InMemorySigner, s conformanceSolution) {
	t.Helper()
	m := &solutionhost.ModuleManifest{Schema: solutionhost.SchemaModuleManifestV1, Binding: s.alias, Alias: s.alias, Kind: solutionhost.KindSolution, Generation: s.generation,
		Host: solutionhost.HostTarget{Coordinate: "conformance-host", Domain: "acme"}, Release: solutionhost.Release{Publisher: "conformance", Name: s.alias, Version: "0.1.0"},
		Workloads: []solutionhost.Workload{{Service: s.alias, Container: "app"}}, Authority: &solutionhost.Authority{Principal: s.alias, Bindings: s.bindings}}
	for _, ceiling := range s.ceilings {
		m.Authority.ResourceKinds = append(m.Authority.ResourceKinds, ceiling.GetResourceKind())
	}
	raw, err := solutionhost.SignedPayload(m)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	document := &compositionv1.AuthorityManifestV1{Module: s.alias, Generation: s.generation, CoreManifestDigest: "sha256:" + hex.EncodeToString(sum[:]),
		ScopeCeilings: s.ceilings, Methods: s.methods}
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

// acknowledgeInstallSteps answers every queued install step as its solution
// does when it accepts the installation: the host's own claim, its leased read
// of the step's authority, and its finish with the solution's 204, which the
// job system projects onto the installation as active.
func acknowledgeInstallSteps(ctx context.Context, svc *business.Service) error {
	for {
		job, err := svc.ClaimInstallationStep(ctx, "conformance-install-step")
		if err != nil || job == nil {
			return err
		}
		lease := job.GetLease()
		ref := &jobsv1.JobLeaseReference{JobId: job.GetId(), WorkerId: lease.GetOwner(), LeaseToken: lease.GetToken()}
		if err := svc.WithInstallationStep(ctx, ref, func(context.Context, *business.InstallationStepAuthority, []*gen.WorkContextScope) error { return nil }); err != nil {
			return fmt.Errorf("install step %s: %w", job.GetId(), err)
		}
		if err := svc.FinishInstallationStep(ctx, ref, job.GetAttemptCount(), 204, ""); err != nil {
			return fmt.Errorf("install step %s: %w", job.GetId(), err)
		}
	}
}

// installationOf is the id of the organisation's installation of target, as
// its administrator lists it through the interface.
func installationOf(t *testing.T, ctx context.Context, conn grpc.ClientConnInterface, adminToken, target string) string {
	t.Helper()
	sess, err := sessionv1.NewSessionServiceClient(conn).SignIn(ctx, &sessionv1.SignInRequest{FixtureToken: adminToken})
	require.NoError(t, err)
	listed, err := installationv1.NewInstallationServiceClient(conn).List(metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+sess.GetBearer()),
		&installationv1.ListRequest{OrganizationId: fixtureOrganization})
	require.NoError(t, err)
	for _, installation := range listed.GetInstallations() {
		if installation.GetTarget() == target {
			require.Equal(t, installationv1.Installation_STATUS_ACTIVE, installation.GetStatus(), "%s is installed and acknowledged", target)
			return installation.GetId()
		}
	}
	t.Fatalf("organization %s holds no installation of %s", fixtureOrganization, target)
	return ""
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

// withdrawnModuleInstallations is the composition's module authority with one
// composed module's installations withdrawn everywhere: a declared workload
// with no active installation, as the suite's absent-organization case needs.
type withdrawnModuleInstallations struct {
	*compositiontest.ComposedModuleInstallations
	withdrawn string
}

// LiveModuleAuthority refuses the withdrawn module as inactive and answers
// every other from the composition.
func (w withdrawnModuleInstallations) LiveModuleAuthority(ctx context.Context, principalID, orgID, installationID string) (*business.LiveModuleAuthority, error) {
	if principalID == business.ModulePrincipalID(w.withdrawn) {
		return nil, fmt.Errorf("%w: %s has no active installation", business.ErrModuleInstallationInactive, w.withdrawn)
	}
	return w.ComposedModuleInstallations.LiveModuleAuthority(ctx, principalID, orgID, installationID)
}

// memoryPolicyLog stands in for the external append-only policy log at its
// seam, idempotent on the operation id as the interface requires, so the
// host's uninstall path runs as it does where a log transport is configured.
type memoryPolicyLog struct {
	mu      sync.Mutex
	records []*business.PolicyLogRecord
}

func (l *memoryPolicyLog) Append(_ context.Context, entry *business.PolicyLogEntry) (*business.PolicyLogReceipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, record := range l.records {
		if record.OperationID == entry.OperationID {
			return &business.PolicyLogReceipt{Receipt: record.Receipt, Sequence: record.Sequence, AppendedAt: record.LoggedAt}, nil
		}
	}
	record := &business.PolicyLogRecord{PolicyLogEntry: *entry, Receipt: "conformance-receipt-" + entry.OperationID,
		Sequence: uint64(len(l.records) + 1), LoggedAt: time.Now().UTC()}
	l.records = append(l.records, record)
	return &business.PolicyLogReceipt{Receipt: record.Receipt, Sequence: record.Sequence, AppendedAt: record.LoggedAt}, nil
}

func (l *memoryPolicyLog) Entries(_ context.Context, after uint64, limit int) ([]*business.PolicyLogRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*business.PolicyLogRecord
	for _, record := range l.records {
		if record.Sequence > after && (limit <= 0 || len(out) < limit) {
			out = append(out, record)
		}
	}
	return out, nil
}
