//go:build !pure

package adapters

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"accounts/fixtures"
	"accounts/internal/deliverytest"
	"accounts/pkg/auth"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	modulecontract "github.com/codefly-dev/core/contracts/module"
	"github.com/codefly-dev/core/sdk"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
)

// An explicit receiver binary is built from the maintained runtime module by its
// owner. It starts Serve, uses the real host Check transport and has a declared
// handler. This opt-in crossing is additional to the always-on SQL fence tests.
func TestCanonicalCombinedInstallQueueThroughRuntime(t *testing.T) {
	binary := os.Getenv("INSTALL_RECEIVER_BINARY")
	if binary == "" {
		t.Skip("INSTALL_RECEIVER_BINARY is required for the maintained receiver process proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dependencyCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	defer stop()
	deps, err := sdk.WithDependencies(dependencyCtx, sdk.WithExcludedDependencies("object-storage", "cache", "vault"), sdk.WithNamingScope("canonical-install-receiver"), sdk.WithTimeout(90*time.Second), sdk.WithSilence("store"))
	require.NoError(t, err)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		require.NoError(t, deps.Destroy(cleanup))
	}()
	_, err = codefly.Init(ctx)
	require.NoError(t, err)
	store, err := infra.NewPostgresStore(ctx)
	require.NoError(t, err)
	defer store.Close()
	svc, err := business.NewService(store)
	require.NoError(t, err)
	emitter, err := business.NewDurableAuditEmitter(store, store)
	require.NoError(t, err)
	defer emitter.Close()
	svc.SetAuditEmitter(emitter)
	svc.SetEntitlementChecker(business.NewDefaultEntitlementChecker(store, business.NoPlansSold))
	require.NoError(t, store.WithControlPlane(ctx, func(ctx context.Context) error { return store.SyncAuditEventTypes(ctx, business.AuditEventCatalog()) }))
	t.Setenv("DEV_FIXTURE_PATH", conformanceFixturePath(t))
	_, err = fixtures.SeedWithInstallations(ctx, svc, conformanceFixtureName)
	require.NoError(t, err)
	signer := deliverytest.Signer(t, "conformance-release")
	verifier := deliverytest.Verifier(t, map[string][]string{signer.KeyID(): {"acme"}})
	svc.SetSolutionDelivery(verifier, store, conformanceCarrier{})
	svc.SetSolutionDeliveryCoordinate("conformance-host")
	svc.SetAuthorityDocumentVerifier(func(doc, envelope []byte) (string, error) {
		return delivery.Verify(doc, envelope, map[string]crypto.PublicKey{signer.KeyID(): signer.Public()})
	})
	svc.SetModuleEnvelopeStore(store)
	svc.SetAdmissionKeyStore(store)
	reconciler, err := business.NewSolutionHostBindingReconciler(svc, business.SolutionHostBindingReconcilerConfig{Source: business.NewDeliveredSolutionHostBindings(svc), Verifier: verifier, Coordinate: "conformance-host"})
	require.NoError(t, err)
	svc.SetCompositionReconcile(reconciler.RunOnce)
	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	require.NoError(t, svc.ConfigureAuthorityCore(ctx, "https://accounts.example.test", "install-test", key))
	pool, err := infra.NewJobWorkerPool(ctx)
	require.NoError(t, err)
	defer pool.Close()
	svc.SetJobOperations(infra.NewPostgresJobStore(pool))
	svc.SetModuleCapabilities(store, infra.NewPostgresJobStore(pool), nil)
	previous, previousToken := service, internalToken
	service = svc
	SetInternalToken("example-install-worker")
	defer func() { service = previous; SetInternalToken(previousToken) }()
	server := grpc.NewServer()
	authorityv1.RegisterAuthorityServiceServer(server, &HostAuthorityServer{})
	// Composition uses the ordinary host adapter; its delivery authentication is
	// the test carrier, while both documents retain real release signatures.
	compositionv1.RegisterCompositionServiceServer(server, HostCompositionSingleton())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	queue := httptest.NewServer(NewInstallationStepHandler(svc))
	defer queue.Close()
	caller := authority.Person{ID: conformanceAlex, Organization: fixtureOrganization}
	core, err := svc.AuthorityCore()
	require.NoError(t, err)
	var edges []modulecontract.EdgeDependency
	for _, principal := range []string{"example-support", "example-records", "example-model", "example-runtime"} {
		deliverModule(t, ctx, conn, signer, conformanceModule{alias: principal, installationRole: "none"})
		edges = append(edges, modulecontract.EdgeDependency{Module: principal, Presence: "required"})
	}
	reused := map[string]business.ModuleInstallationPrerequisite{}
	for _, outcome := range []string{"refuse", "ack", "delayed", "fail", "revoked", "new-release"} {
		t.Run(outcome, func(t *testing.T) {
			api := &InstallationServer{}
			personContext := stampVerifiedIdentity(ctx, caller.ID, fixtureOrganization, auth.Assurance{})
			historical := outcome == "revoked" || outcome == "new-release"
			var historicalTarget string
			var historicalRevision uint64
			revokeSupport := func() {
				target, err := svc.ResolveInstallationPrincipal(ctx, fixtureOrganization, "example-support")
				require.NoError(t, err)
				var installed *gen.Installation
				if previous, ok := reused[target.TargetID]; ok {
					installed, _, err = svc.GetInstallation(ctx, fixtureOrganization, previous.InstallationID)
				} else {
					installed, _, err = svc.InstallFixtureTarget(ctx, caller.ID, fixtureOrganization, "example-support", "", "")
				}
				require.NoError(t, err)
				_, err = api.UninstallSolution(personContext, &gen.UninstallSolutionRequest{OrgId: fixtureOrganization, InstallationId: installed.Id})
				require.NoError(t, err)
				row, _, err := svc.GetInstallation(ctx, fixtureOrganization, installed.Id)
				require.NoError(t, err)
				require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_REVOKED, row.Status)
				historicalTarget, historicalRevision = row.TargetId, row.Revision
				delete(reused, row.TargetId)
			}
			if historical {
				revokeSupport()
				if outcome == "new-release" {
					deliverModule(t, ctx, conn, signer, conformanceModule{alias: "example-support", generation: 2, releaseVersion: "0.2.0", installationRole: "none"})
				}
			}
			alias := "example-setup-" + outcome
			deliverSolution(t, ctx, conn, signer, conformanceSolution{alias: alias, generation: 1, installationRole: "none", ceilings: []*authorityv1.WorkScopeV1{wireScope("example.item", "read")}, roles: map[string][]*authorityv1.WorkScopeV1{"installer": {wireScope("example.item", "read")}}, edges: edges})
			target, err := svc.ResolveInstallationPrincipal(ctx, fixtureOrganization, alias)
			require.NoError(t, err)
			review, err := api.ReviewSolutionInstall(personContext, &gen.ReviewSolutionInstallRequest{OrgId: fixtureOrganization, TargetId: target.TargetID})
			require.NoError(t, err)
			require.Len(t, review.Modules, 4)
			if historical {
				for _, module := range review.Modules {
					if module.TargetId != historicalTarget {
						continue
					}
					if module.Approval.ExpectedRevision == 0 {
						_, refusal := api.InstallReviewedSolution(personContext, &gen.InstallReviewedSolutionRequest{OrgId: fixtureOrganization, Review: review})
						require.ErrorContains(t, refusal, "INSTALLATION_APPROVAL_REVISION_CHANGED")
						t.Log("negative control: public review offered zero and public install refused historical revision")
					}
					require.Equal(t, historicalRevision, module.Approval.ExpectedRevision)
					require.Empty(t, module.ReuseInstallationId)
				}
				if outcome == "revoked" {
					revokeSupport()
					_, err = api.InstallReviewedSolution(personContext, &gen.InstallReviewedSolutionRequest{OrgId: fixtureOrganization, Review: review})
					require.ErrorContains(t, err, "review changed")
					review, err = api.ReviewSolutionInstall(personContext, &gen.ReviewSolutionInstallRequest{OrgId: fixtureOrganization, TargetId: target.TargetID})
					require.NoError(t, err)
					for _, module := range review.Modules {
						if module.TargetId == historicalTarget {
							require.Equal(t, historicalRevision, module.Approval.ExpectedRevision)
						}
					}
				}
			}
			// The generated JSON shape is the manual API request, including
			// uint64 approvals and the unchanged module scope string.
			wire, err := protojson.Marshal(&gen.InstallReviewedSolutionRequest{OrgId: fixtureOrganization, Review: review})
			require.NoError(t, err)
			decoded := &gen.InstallReviewedSolutionRequest{}
			require.NoError(t, protojson.Unmarshal(wire, decoded))
			review = decoded.Review
			for _, module := range review.Modules {
				previous := reused[module.TargetId]
				require.Equal(t, previous.InstallationID, module.ReuseInstallationId)
				if previous.InstallationID != "" {
					require.Equal(t, previous.Revision, module.Approval.ExpectedRevision)
				}
			}
			receiverOutcome := outcome
			if outcome == "delayed" || historical {
				receiverOutcome = "ack"
			}
			receiver, record := startCanonicalReceiver(t, binary, review.Approval.TargetId, listener.Addr().String(), receiverOutcome)
			type result struct {
				inst domain.Installation
				err  error
			}
			resultCh := make(chan result, 1)
			go func() {
				if !historical {
					inst, err := svc.InstallReviewedSolution(ctx, caller, fixtureOrganization, review)
					resultCh <- result{inst, err}
					return
				}
				_, installErr := api.InstallReviewedSolution(personContext, decoded)
				rows, readErr := core.Installations(ctx, caller, fixtureOrganization)
				for _, inst := range rows {
					if inst.Target == alias {
						resultCh <- result{inst, installErr}
						return
					}
				}
				resultCh <- result{err: fmt.Errorf("public install: %v; canonical read: %v", installErr, readErr)}
			}()
			var claim canonicalProcessClaim
			deadline := time.Now().Add(25 * time.Second)
			for {
				status, body := callCanonicalQueue(t, queue.URL, "claim", map[string]any{"worker": "example-process"})
				if status == 200 {
					require.NoError(t, json.Unmarshal(body, &claim))
					break
				}
				require.Equal(t, 204, status, string(body))
				select {
				case result := <-resultCh:
					t.Fatalf("installation ended before queue claim: %v", result.err)
				default:
				}
				require.True(t, time.Now().Before(deadline), "no setup job claimed")
				time.Sleep(25 * time.Millisecond)
			}
			var strict map[string]any
			require.NoError(t, json.Unmarshal(claim.Installation, &strict))
			require.Len(t, strict, 3)
			var timedOut *result
			if outcome == "delayed" {
				ended := <-resultCh
				require.Error(t, ended.err)
				require.Equal(t, domain.Pending, ended.inst.Status)
				timedOut = &ended
			}
			status, reason := deliverCanonicalClaim(t, receiver, claim)
			expected := map[string]int{"ack": 204, "delayed": 204, "refuse": 409, "fail": 503, "revoked": 204, "new-release": 204}[outcome]
			require.Equal(t, expected, status, reason)
			if outcome == "ack" || historical {
				again, _ := deliverCanonicalClaim(t, receiver, claim)
				require.Equal(t, 204, again)
			}
			finished, body := callCanonicalQueue(t, queue.URL, "finish", map[string]any{"job_id": claim.JobID, "worker": claim.Worker, "lease_token": claim.Lease, "attempt": claim.Attempt, "status": status, "reason": reason})
			require.Equal(t, 204, finished, string(body))
			var done result
			if timedOut != nil {
				done = *timedOut
			} else {
				done = <-resultCh
			}
			if outcome == "ack" || historical {
				require.NoError(t, done.err)
				require.Equal(t, domain.Active, done.inst.Status)
			} else {
				require.Error(t, done.err)
				require.Equal(t, domain.Pending, done.inst.Status)
			}
			plan, err := store.SolutionInstallationPlan(ctx, fixtureOrganization, done.inst.ID)
			require.NoError(t, err)
			require.Len(t, plan.Dependencies, 4)
			for _, dependency := range plan.Dependencies {
				require.True(t, dependency.NoStandingAuthority)
				require.Empty(t, dependency.RoleID)
				if previous, ok := reused[dependency.TargetID]; ok {
					require.Equal(t, previous, dependency, "reuse preserves each separate consent receipt and revision")
				}
				reused[dependency.TargetID] = dependency
			}
			require.Equal(t, review.Approval.OperationId, done.inst.Consent.OperationID)
			recorded, err := os.ReadFile(record)
			require.NoError(t, err)
			require.Len(t, strings.Split(strings.TrimSpace(string(recorded)), "\n"), 1, "ACK redelivery must verify afresh without invoking handler again")
			if outcome == "delayed" {
				read, _, err := svc.GetInstallation(ctx, fixtureOrganization, done.inst.ID)
				require.NoError(t, err)
				require.Equal(t, "succeeded", read.SetupState)
				require.Contains(t, read.SetupMessage, "Continue")
				require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_PENDING, read.Status, "late ACK alone never activates")
				resumed, err := svc.InstallReviewedSolution(ctx, caller, fixtureOrganization, review)
				require.NoError(t, err)
				require.Equal(t, domain.Active, resumed.Status)
				require.Equal(t, done.inst.Consent, resumed.Consent)
				done = result{resumed, nil}
			} else if outcome != "ack" && !historical {
				// The current administrator resumes the SAME review. Neither a
				// worker nor a successful job impersonates the approving user.
				recovered, _ := startCanonicalReceiver(t, binary, review.Approval.TargetId, listener.Addr().String(), "ack")
				go func() {
					inst, err := svc.InstallReviewedSolution(ctx, caller, fixtureOrganization, review)
					resultCh <- result{inst, err}
				}()
				var retry canonicalProcessClaim
				deadline = time.Now().Add(25 * time.Second)
				for {
					status, body := callCanonicalQueue(t, queue.URL, "claim", map[string]any{"worker": "example-process"})
					if status == 200 {
						require.NoError(t, json.Unmarshal(body, &retry))
						break
					}
					require.Equal(t, 204, status, string(body))
					select {
					case ended := <-resultCh:
						t.Fatalf("continuation ended before delivery: %v", ended.err)
					default:
					}
					require.True(t, time.Now().Before(deadline))
					time.Sleep(25 * time.Millisecond)
				}
				require.JSONEq(t, string(claim.Installation), string(retry.Installation), "retry preserves exact canonical ID/org/revision")
				code, reason := deliverCanonicalClaim(t, recovered, retry)
				require.Equal(t, 204, code, reason)
				code, body := callCanonicalQueue(t, queue.URL, "finish", map[string]any{"job_id": retry.JobID, "worker": retry.Worker, "lease_token": retry.Lease, "attempt": retry.Attempt, "status": code})
				require.Equal(t, 204, code, string(body))
				resumed := <-resultCh
				require.NoError(t, resumed.err)
				require.Equal(t, domain.Active, resumed.inst.Status)
				require.Equal(t, done.inst.Consent, resumed.inst.Consent)
				require.Equal(t, done.inst.Revision, resumed.inst.Revision)
				done = resumed
			}
			replayed, err := svc.InstallReviewedSolution(ctx, caller, fixtureOrganization, review)
			require.NoError(t, err)
			require.Equal(t, done.inst, replayed)
			projection, err := api.InstallReviewedSolution(personContext, decoded)
			require.NoError(t, err)
			require.Equal(t, done.inst.ID, projection.Id)
			require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, projection.Status)
			quiet, body := callCanonicalQueue(t, queue.URL, "claim", map[string]any{"worker": "example-process"})
			require.Equal(t, 204, quiet, string(body))
			if outcome == "ack" || historical {
				after, _ := deliverCanonicalClaim(t, receiver, claim)
				require.Equal(t, 503, after, "active credentials cannot replay cached setup ACK")
				require.NoError(t, core.Uninstall(ctx, caller, fixtureOrganization, done.inst.ID))
				for _, dependency := range plan.Dependencies {
					current, err := store.ModuleInstallationPrerequisite(ctx, fixtureOrganization, dependency.InstallationID, dependency.EnvelopeRevision)
					require.NoError(t, err, "uninstall keeps each shared module")
					require.Equal(t, dependency, current)
				}
			}
		})
	}
}

type canonicalProcessClaim struct {
	JobID        string          `json:"job_id"`
	Worker       string          `json:"worker"`
	Lease        string          `json:"lease_token"`
	Attempt      uint32          `json:"attempt"`
	Token        string          `json:"work_context"`
	Installation json.RawMessage `json:"installation"`
}

func callCanonicalQueue(t *testing.T, base, method string, value any) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, base+InstallationStepPath+method, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Codefly-Internal-Token", "example-install-worker")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, err = buf.ReadFrom(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, buf.Bytes()
}
func deliverCanonicalClaim(t *testing.T, base string, claim canonicalProcessClaim) (int, string) {
	t.Helper()
	var step business.InstallationStep
	require.NoError(t, json.Unmarshal(claim.Installation, &step))
	req, err := http.NewRequest(http.MethodPost, base+"/_codefly/installations", bytes.NewReader(claim.Installation))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-codefly-work-context", claim.Token)
	req.Header.Set("x-codefly-installation-id", step.InstallationID)
	req.Header.Set("x-codefly-installation-revision", strconv.FormatUint(step.Revision, 10))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var refusal struct {
		Reason string `json:"reason"`
	}
	if resp.StatusCode == 409 {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&refusal))
	}
	return resp.StatusCode, refusal.Reason
}
func startCanonicalReceiver(t *testing.T, binary, principal, authorityAddr, outcome string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	token, record := filepath.Join(dir, "token"), filepath.Join(dir, "installations.jsonl")
	require.NoError(t, os.WriteFile(token, []byte("example-workload-credential"), 0600))
	free, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := free.Addr().String()
	_, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	require.NoError(t, free.Close())
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "SOLUTION_ID="+principal, "INSTALL_RECEIVER_LOG="+record, "INSTALL_RECEIVER_OUTCOME="+outcome,
		"CODEFLY__MODULE=receiver", "CODEFLY__SERVICE=backend", "CODEFLY__ENVIRONMENT=local", "CODEFLY__ENDPOINT__RECEIVER__BACKEND__HTTP__HTTP="+addr, "PORT="+port,
		"CODEFLY__WORKSPACE_CONFIGURATION__AUTHORITY__ENDPOINT=grpc://"+authorityAddr, "CODEFLY__WORKLOAD_TOKEN_FILE="+token, "CODEFLY__CONTRACT_PROFILE=local", "ASSETS_DIR="+dir)
	log, err := os.Create(filepath.Join(dir, "receiver.log"))
	require.NoError(t, err)
	cmd.Stdout, cmd.Stderr = log, log
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = log.Close() })
	base := "http://" + addr
	for i := 0; i < 100; i++ {
		resp, err := http.Get(base + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return base, record
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("receiver %s did not start; log %s", outcome, log.Name()))
	return "", ""
}
