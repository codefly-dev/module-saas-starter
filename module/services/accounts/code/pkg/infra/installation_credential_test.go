//go:build !pure

package infra_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"accounts/pkg/adapters"
	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"
	"accounts/pkg/datasource/github"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Only the external content provider is faked. Installation consent, scope,
// leases, source reads, artifact receipts and revocation all use PostgreSQL.
type installationContent struct {
	business.GitHubContentClient
	business.GitHubRepository
}

const installationVersion = "0123456789abcdef0123456789abcdef01234567"
const installationBlob = "89abcdef0123456789abcdef0123456789abcdef"

var installationBytes = []byte("Example content\n")

func (installationContent) OpenRepository(context.Context, github.Workspace, string) (business.GitHubRepository, error) {
	return installationContent{}, nil
}
func (installationContent) List(_ context.Context, _ string, match func(string) bool) ([]github.File, error) {
	if !match("example.md") {
		return nil, fmt.Errorf("fixture file excluded")
	}
	return []github.File{{Path: "example.md", SHA: installationBlob, Size: int64(len(installationBytes))}}, nil
}
func (installationContent) Fetch(context.Context, []string) error { return nil }
func (installationContent) Sizes(context.Context, []string) (map[string]int64, error) {
	return map[string]int64{installationBlob: int64(len(installationBytes))}, nil
}
func (installationContent) Read(context.Context, string, int64) ([]byte, error) {
	return installationBytes, nil
}
func (installationContent) Stream(_ context.Context, ids []string, visit func(string, int64, io.Reader) error) error {
	for _, id := range ids {
		if err := visit(id, int64(len(installationBytes)), bytes.NewReader(installationBytes)); err != nil {
			return err
		}
	}
	return nil
}
func (installationContent) Fetches() int { return 0 }
func (installationContent) Close() error { return nil }

type installationCipher struct{}

func (installationCipher) EncryptSecret(context.Context, string, string) (string, error) {
	return "example-envelope", nil
}
func (installationCipher) DecryptSecret(context.Context, string, string) (string, error) {
	return "example-token", nil
}

// The database evidence that a module credential is held to the installation it
// was minted for. It retains the real signed credential across an uninstall and a
// reinstall: the issuer stops verifying it the moment its installation is no
// longer active, every business entrypoint refuses an identity verified from it
// before then, and a reinstall — new consent under a new installation id — does
// not revive it. It does not claim to exercise gateway caching.
func TestSealedModuleCredentialDBLiveInstallationAtEveryEntryPoint(t *testing.T) {
	principal := business.ModulePrincipalID("example")
	queue := executionQueue("credential-live")
	var grant business.ModulePrincipalGrant
	var policy business.ExecutableArtifactPolicy
	var installParams business.InstallSolutionParams
	org, owner, role, installation, root := installFixture(t, "*", "*", func(p *business.InstallSolutionParams) {
		policy = business.ExecutableArtifactPolicy{Schema: "example.artifact/v1", Sources: map[string]string{"acme.example": p.RegistryBindingID}, Activate: business.ArtifactPermission{Resource: "definitions", Action: "configure"}, Run: business.ArtifactPermission{Resource: "definitions", Action: "configure"}, Contracts: []business.ArtifactContract{{Kind: "model", Name: "example/profile", Digest: "sha256:" + strings.Repeat("a", 64)}}, RequiredKinds: []string{"model"}}
		grant = business.ModulePrincipalGrant{Prefix: "example", Tenant: p.OrgID, PresenceBinding: p.RegistryBindingID,
			Queues: []string{queue, business.DatasourceIngestQueue}, Resources: []string{"records"},
			ReadAudiences:      map[string]business.ModuleReadAudience{"read": {Audience: "example.read", Scopes: []business.ModuleReadScope{{ResourceKind: "records"}}}},
			OperationAudiences: map[string]business.ModuleOperationAudience{"operate": {Audience: "example.api", InvokeScopes: []business.ModuleOperationScope{{ResourceKind: "records", Actions: []string{"read", "write"}}}, LookupScopes: []business.ModuleOperationScope{{ResourceKind: "records", Actions: []string{"read"}}}}},
			ArtifactPolicies:   map[string]business.ExecutableArtifactPolicy{"content": policy}}
		scope := business.FullInstallationScope(grant)
		p.RegistryPrincipalID, p.Scope = principal, &scope
		installParams = *p
	})
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.GrantScope(ctx, &gen.ScopeGrant{Id: business.NewIDString(), OrgId: org, SubjectId: owner, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: root, RoleId: role})
	}))
	pool, worker := newJobExecutionHarness(t)
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	svc.SetModuleCapabilities(testStore, worker, nil)
	svc.InstallDerivedModulePrincipals(business.ModulePrincipalRegistry{principal: grant}, installParams.EnvelopeRevision, nil)
	svc.SetDatasourceConnector(installationCipher{}, testStore, "")
	svc.SetDatasourceGitHubClientFactory(func(string) business.GitHubContentClient { return installationContent{} })
	source := seedDatasourceSource(t, org)
	svc.SetModuleAuthorityReads(testStore, nil)
	svc.SetWorkContextSealStore(testStore)
	cluster := executiontest.New()
	svc.SetExecutionBinding(cluster, cluster)
	cluster.Approve("example", executiontest.Digest("example"), 1)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	issuer := &adapters.WorkContextAuthorityServer{}
	issuer.Configure(adapters.WorkContextAuthorityConfiguration{Issuer: "accounts.test", KeyID: "example-key", PrivateKey: key, Authority: testStore, Seals: svc})
	credential, _, err := issuer.StartModuleTask(testCtx, business.ModuleWorkContextAuthority{
		PrincipalID: principal, Tenant: org, InstallationID: installation.Id, InstallationRevision: 1,
		Execution: business.ExecutionIdentity{Running: business.RunningDigest(executiontest.Image("example")), Incarnation: 1},
	})
	require.NoError(t, err, "the module's principal holds an epoch from its installation (migration 35)")
	verified, err := issuer.VerifyModuleWorkContext(testCtx, credential)
	require.NoError(t, err)
	require.NotNil(t, verified.Sealed)
	require.Equal(t, installation.Id, *verified.Sealed.InstallationID)
	caller := func(*testing.T) business.ModuleCaller { return verified }
	ctx := auth.WithVerifiedDatabaseIdentity(testCtx, owner, org)
	enqueue := &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{Direction: jobsv1.JobDirection_JOB_DIRECTION_OUTBOX, Scope: &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{OrganizationId: org}}, Queue: queue, Topic: "example.work", Source: "example", IdempotencyKey: business.NewIDString(), SchemaVersion: 1, ContentType: "application/json", Payload: []byte(`{}`), MaxAttempts: 3}}
	artifact := business.ExecutableArtifactRequest{Installation: installation.Id, Policy: "content", Identity: business.ExecutableArtifactIdentity{Schema: policy.Schema, Source: "acme.example", Subject: []byte(`{"version":1}`), ExpectedRevision: 1, Contracts: policy.Contracts}}
	var lease *jobsv1.JobLeaseReference
	prepareLease := func() *jobsv1.JobLeaseReference {
		job := proto.Clone(enqueue.Job).(*jobsv1.NewJob)
		job.IdempotencyKey = business.NewIDString()
		_, err := svc.ModuleEnqueueJob(ctx, caller(t), org, &jobsv1.EnqueueJobRequest{Job: job})
		require.NoError(t, err)
		out, err := svc.ModuleClaimJobs(ctx, caller(t), claimExecutionRequest(queue, "example-worker", 1))
		require.NoError(t, err)
		require.Len(t, out.Jobs, 1)
		return executionLease(out.Jobs[0])
	}
	ackLease, retryLease, deadLease := prepareLease(), prepareLease(), prepareLease()
	paths := []struct {
		name string
		use  func(*testing.T) error
	}{
		{"enqueue", func(t *testing.T) error { _, err := svc.ModuleEnqueueJob(ctx, caller(t), org, enqueue); return err }},
		{"claim", func(t *testing.T) error {
			out, err := svc.ModuleClaimJobs(ctx, caller(t), claimExecutionRequest(queue, "example-worker", 1))
			if err != nil {
				return err
			}
			require.Len(t, out.Jobs, 1)
			lease = executionLease(out.Jobs[0])
			var consumer string
			require.NoError(t, pool.QueryRow(ctx, `SELECT consumer_authority->>'codefly.installation_id' FROM job_messages WHERE id=$1`, lease.JobId).Scan(&consumer))
			require.Equal(t, installation.Id, consumer)
			return nil
		}},
		{"heartbeat", func(t *testing.T) error {
			_, err := svc.ModuleHeartbeatJob(ctx, caller(t), &jobsv1.HeartbeatJobRequest{Lease: lease, Extension: durationpb.New(time.Minute)})
			return err
		}},
		{"ack", func(t *testing.T) error { return svc.ModuleAckJob(ctx, caller(t), ackLease, "", "") }},
		{"retry", func(t *testing.T) error {
			return svc.ModuleNackJob(ctx, caller(t), retryLease, &jobsv1.JobFailure{Code: "example.retry"}, true, timestamppb.New(time.Now().Add(time.Hour)))
		}},
		{"dead-letter", func(t *testing.T) error {
			return svc.ModuleNackJob(ctx, caller(t), deadLease, &jobsv1.JobFailure{Code: "example.permanent"}, false, nil)
		}},
		{"fetch-blob", func(t *testing.T) error {
			content, _, err := svc.ModuleFetchDatasourceBlob(ctx, caller(t), source, installationBlob)
			if err == nil {
				require.Equal(t, installationBytes, content)
			}
			return err
		}},
		{"fetch-files", func(t *testing.T) error {
			visits := 0
			err := svc.ModuleFetchDatasourceFiles(ctx, caller(t), source, installationVersion, []business.DatasourceFileRequest{{ItemID: "example.md", ItemVersion: installationBlob}}, func(_ business.DatasourceFile, r io.Reader) error {
				visits++
				content, err := io.ReadAll(r)
				require.Equal(t, installationBytes, content)
				return err
			})
			if err == nil {
				require.Equal(t, 1, visits)
			} else {
				require.Zero(t, visits)
			}
			return err
		}},
		{"delegated-read-binding", func(t *testing.T) error {
			_, err := svc.ModuleReadAudience(ctx, caller(t), org, "example", "read")
			return err
		}},
		{"delegated-operation-binding", func(t *testing.T) error {
			_, err := svc.ModuleOperationAudience(ctx, caller(t), org, "example", "operate")
			return err
		}},
		{"approve-artifact", func(t *testing.T) error {
			_, err := svc.DecideExecutableArtifact(ctx, caller(t), org, owner, "example", "approve", artifact)
			return err
		}},
		{"authorize-artifact", func(t *testing.T) error {
			_, err := svc.DecideExecutableArtifact(ctx, caller(t), org, owner, "example", "authorize", artifact)
			return err
		}},
	}
	for _, path := range paths {
		t.Run("active/"+path.name, func(t *testing.T) { require.NoError(t, path.use(t)) })
	}
	proxy := func() *gen.ListSolutionEntitlementsResponse {
		out, err := svc.ListSolutionEntitlements(ctx, &gen.ListSolutionEntitlementsRequest{OrgId: caller(t).BoundOrg, SubjectId: owner})
		require.NoError(t, err)
		return out
	}
	require.Len(t, proxy().Entitlements, 1, "the gateway's fresh entitlement read admits this installation")
	require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
		_, _, err := testStore.UninstallSolution(ctx, org, installation.Id)
		return err
	}))
	_, err = issuer.VerifyModuleWorkContext(testCtx, credential)
	require.Error(t, err, "the issuer stops verifying a credential whose installation is gone")
	for _, path := range paths {
		t.Run("uninstalled/"+path.name, func(t *testing.T) { require.ErrorIs(t, path.use(t), business.ErrModuleInstallationInactive) })
	}
	require.Empty(t, proxy().Entitlements, "the next fresh module proxy entitlement read refuses")
	// Reinstall is NEW consent, under a new installation id. The old credential
	// names the installation it was minted for, so neither the issuer nor any
	// entrypoint accepts it again.
	var replacement *gen.Installation
	require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
		var err error
		replacement, err = testStore.InstallSolution(ctx, &installParams)
		return err
	}))
	acknowledgeInstallation(t, replacement)
	require.NotEqual(t, installation.Id, replacement.Id)
	_, err = issuer.VerifyModuleWorkContext(testCtx, credential)
	require.Error(t, err, "a reinstall does not revive a credential sealed to the old installation")
	// An identity verified from it before the uninstall is refused too: the live
	// read is made for the installation the credential is sealed to, and that one
	// is not active, whatever replaced it.
	enqueue.Job.IdempotencyKey = business.NewIDString()
	_, err = svc.ModuleEnqueueJob(ctx, caller(t), org, enqueue)
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive)
}

func TestHostJobsNeedNoModuleConsumerAuthority(t *testing.T) {
	pool, store := newJobExecutionHarness(t)
	for _, scope := range []*jobsv1.JobScope{{Value: &jobsv1.JobScope_Global{Global: true}}, {Value: &jobsv1.JobScope_SubjectId{SubjectId: seedUser(t)}}} {
		queue := executionQueue("host-authority")
		req := &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{Direction: jobsv1.JobDirection_JOB_DIRECTION_OUTBOX, Scope: scope, Queue: queue, Topic: "example.host", Source: "accounts", IdempotencyKey: business.NewIDString(), SchemaVersion: 1, Payload: []byte(`{}`), ContentType: "application/json", MaxAttempts: 3}}
		enqueued, err := store.EnqueueJob(testCtx, req)
		require.NoError(t, err)
		handled := false
		worker, err := jobs.NewWorker(jobs.WorkerConfig{Store: store, Queue: queue, WorkerID: "example-host-worker", Handler: func(ctx context.Context, envelope *jobsv1.JobEnvelope) error {
			_, module := jobs.ModuleClaim(ctx)
			require.False(t, module)
			require.NotContains(t, envelope.Attributes, "codefly.installation_id")
			_, err := store.Heartbeat(ctx, &jobsv1.HeartbeatJobRequest{Lease: executionLease(envelope), Extension: durationpb.New(time.Minute)})
			handled = true
			return err
		}})
		require.NoError(t, err)
		count, err := worker.RunOnce(testCtx)
		require.NoError(t, err)
		require.Equal(t, 1, count)
		require.True(t, handled)
		var state string
		var noAuthority bool
		require.NoError(t, pool.QueryRow(testCtx, `SELECT state,consumer_authority IS NULL FROM job_messages WHERE id=$1`, enqueued.JobId).Scan(&state, &noAuthority))
		require.Equal(t, "succeeded", state)
		require.True(t, noAuthority)
	}
}
