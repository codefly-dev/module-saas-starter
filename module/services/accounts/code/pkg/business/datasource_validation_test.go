package business_test

import (
	"accounts/pkg/business"
	"accounts/pkg/datasource/github"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type validationGitHub struct {
	fakeGitHub
	repositoryErr, branchErr error
}

func (f *validationGitHub) DefaultBranch(context.Context, string) (string, error) {
	return "main", f.repositoryErr
}
func (f *validationGitHub) ResolveCommit(context.Context, string, string) (string, error) {
	return "abc", f.branchErr
}

func TestGitHubValidationRejectsBeforeSaving(t *testing.T) {
	for _, tt := range []struct {
		name               string
		repoErr, branchErr error
		message            string
	}{
		{"invalid token", github.ErrUnauthorized, nil, "401"},
		{"permissions", github.ErrForbidden, nil, "permissions"},
		{"missing repository", github.ErrNotFound, nil, "404"},
		{"missing branch", nil, github.ErrNotFound, "branch"},
		{"network failure", errors.New("secret-sensitive-response"), nil, "unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newDatasourceFakeStore()
			producer := &recordingProducer{}
			svc, _ := newDatasourceService(store, producer, &validationGitHub{repositoryErr: tt.repoErr, branchErr: tt.branchErr})
			_, err := svc.AddGitHubSource(context.Background(), "actor", business.AddGitHubSourceInput{OrgID: testOrg, Repo: "acme/docs", CollectionLabel: "docs", AccessToken: "secret-sensitive-token"})
			if err == nil || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), "secret-sensitive") {
				t.Fatal("secret leaked")
			}
			if len(store.sources) != 0 || len(store.collections) != 0 || len(producer.jobs) != 0 {
				t.Fatal("validation failure persisted source, collection, or job")
			}
		})
	}
}

func TestGitHubValidationRechecksSavedTokenBeforeSync(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &recordingProducer{}
	client := &validationGitHub{}
	svc, _ := newDatasourceService(store, producer, client)
	source := addSource(t, svc, business.AddGitHubSourceInput{OrgID: testOrg, Repo: "acme/docs", CollectionLabel: "docs", AccessToken: "valid-at-connect"})
	client.repositoryErr = github.ErrUnauthorized
	_, err := svc.SyncDatasourceSource(context.Background(), "actor", testOrg, source.ID)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(producer.jobs) != 0 {
		t.Fatal("queued sync with a rejected credential")
	}
}

type failingValidationCipher struct {
	purposeCipher
	err error
}

func (c failingValidationCipher) DecryptSecret(context.Context, string, string) (string, error) {
	return "", c.err
}

func TestGitHubSyncPreflightKeepsTransientFailuresDurable(t *testing.T) {
	for _, tt := range []struct {
		name                   string
		providerErr, cipherErr error
		wantCode               codes.Code
	}{
		{"network", errors.New("private provider response"), nil, codes.OK},
		{"provider timeout", context.DeadlineExceeded, nil, codes.OK},
		{"rate limit", github.ErrRateLimited, nil, codes.OK},
		{"vault unavailable", nil, errors.New("private vault response"), codes.OK},
		{"vault timeout", nil, context.DeadlineExceeded, codes.OK},
		{"vault rejected ciphertext", nil, vaultStatusError(400), codes.FailedPrecondition},
		{"vault forbidden", nil, vaultStatusError(403), codes.OK},
		{"bad credential", github.ErrUnauthorized, nil, codes.FailedPrecondition},
		{"permissions", github.ErrForbidden, nil, codes.FailedPrecondition},
		{"missing ref", github.ErrNotFound, nil, codes.FailedPrecondition},
		{"invalid envelope", nil, business.ErrInvalidSecretEnvelope, codes.FailedPrecondition},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newDatasourceFakeStore()
			producer := &recordingProducer{}
			client := &validationGitHub{}
			svc, _ := newDatasourceService(store, producer, client)
			source := addSource(t, svc, business.AddGitHubSourceInput{OrgID: testOrg, Repo: "acme/docs", Branch: "main", CollectionLabel: "docs", AccessToken: "token"})
			store.sources[source.ID].LastIngestedCommit = "abc"
			client.branchErr = tt.providerErr
			if tt.cipherErr != nil {
				svc.SetDatasourceConnector(failingValidationCipher{err: tt.cipherErr}, producer, "")
			}
			jobID, err := svc.SyncDatasourceSource(context.Background(), "actor", testOrg, source.ID)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("code: %v, error: %v", status.Code(err), err)
			}
			if tt.wantCode != codes.OK {
				if len(producer.jobs) != 0 {
					t.Fatal("queued a definitively invalid source")
				}
				return
			}
			if jobID == "" || len(producer.jobs) != 1 {
				t.Fatal("lost forced sync during a transient failure")
			}
			job := producer.jobs[0]
			if job.GetQueue() != business.DatasourceDeliveryQueue || job.GetAttributes()["datasource.reconcile_mode"] != "force" || job.GetMaxAttempts() < 2 {
				t.Fatalf("not a retryable forced reconcile: %v", job)
			}
			// Recovery must still attempt a snapshot when the head equals the cursor.
			client.branchErr = nil
			svc.SetDatasourceConnector(purposeCipher{}, producer, "")
			err = svc.NewDatasourceDeliveryJobHandler()(context.Background(), &jobsv1.JobEnvelope{
				Queue: job.GetQueue(), Topic: job.GetTopic(), Attributes: job.GetAttributes(),
			})
			if err != nil || len(producer.jobs) != 2 {
				t.Fatalf("repair not dispatched after recovery: %v", err)
			}
		})
	}
}

func TestGitHubSyncCanceledRequestDoesNotEnqueue(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &recordingProducer{}
	svc, _ := newDatasourceService(store, producer, nil)
	source := addSource(t, svc, business.AddGitHubSourceInput{OrgID: testOrg, Repo: "acme/docs", CollectionLabel: "docs", AccessToken: "token"})
	svc.SetDatasourceConnector(failingValidationCipher{err: context.Canceled}, producer, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.SyncDatasourceSource(ctx, "actor", testOrg, source.ID)
	if status.Code(err) != codes.Canceled || len(producer.jobs) != 0 {
		t.Fatalf("canceled request: %v, jobs %d", err, len(producer.jobs))
	}
}

func TestGitHubExplicitRefDoesNotRequireDefaultBranchLookup(t *testing.T) {
	client := &validationGitHub{repositoryErr: errors.New("default branch lookup must not run")}
	svc, _ := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, client)
	_, err := svc.AddGitHubSource(context.Background(), "actor", business.AddGitHubSourceInput{OrgID: testOrg, Repo: "acme/docs", Branch: "release", CollectionLabel: "docs", AccessToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.AddSource(context.Background(), "actor", business.AddSourceInput{OrgID: testOrg, Provider: business.DatasourceProviderGitHub, Repo: "acme/docs", Branch: "release", CollectionLabel: "docs", Credential: "token"})
	if err != nil {
		t.Fatal(err)
	}
	client.branchErr = github.ErrRateLimited
	_, err = svc.AddSource(context.Background(), "actor", business.AddSourceInput{OrgID: testOrg, Provider: business.DatasourceProviderGitHub, Repo: "acme/docs", Branch: "release", CollectionLabel: "docs", Credential: "token"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("connect must reject an unvalidated credential: %v", err)
	}
}

type failedSyncProducer struct{}

func (failedSyncProducer) EnqueueJob(context.Context, *jobsv1.EnqueueJobRequest) (*jobsv1.EnqueueJobResponse, error) {
	return nil, errors.New("queue unavailable")
}

func TestGitHubTransientPreflightDoesNotHideEnqueueFailure(t *testing.T) {
	svc, _ := newDatasourceService(newDatasourceFakeStore(), &recordingProducer{}, nil)
	source := addSource(t, svc, business.AddGitHubSourceInput{OrgID: testOrg, Repo: "acme/docs", CollectionLabel: "docs", AccessToken: "token"})
	svc.SetDatasourceConnector(failingValidationCipher{err: errors.New("vault unavailable")}, failedSyncProducer{}, "")
	id, err := svc.SyncDatasourceSource(context.Background(), "actor", testOrg, source.ID)
	if err == nil || id != "" {
		t.Fatalf("acknowledged a repair that was not persisted: id %q, error %v", id, err)
	}
}

type vaultStatusError int

func (e vaultStatusError) Error() string       { return "provider-body-secret" }
func (e vaultStatusError) HTTPStatusCode() int { return int(e) }

type brokenDatasourceCipher struct {
	purposeCipher
	err error
}

func (c brokenDatasourceCipher) DecryptSecret(context.Context, string, string) (string, error) {
	return "", c.err
}

func TestGitHubReconnectPreservesSourceAndRejectsInvalidReplacement(t *testing.T) {
	store := newDatasourceFakeStore()
	producer := &recordingProducer{}
	client := &validationGitHub{}
	svc, audit := newDatasourceService(store, producer, client)
	source := addSource(t, svc, business.AddGitHubSourceInput{OrgID: testOrg, Repo: "acme/docs", CollectionLabel: "docs", AccessToken: "old"})
	original := source.CredentialSecretRef
	svc.SetDatasourceConnector(brokenDatasourceCipher{err: vaultStatusError(400)}, producer, "")
	svc.SetDatasourceGitHubClientFactory(func(token string) business.GitHubContentClient {
		if token != "replacement" {
			t.Fatal("did not validate replacement")
		}
		return client
	})
	client.branchErr = github.ErrUnauthorized
	if _, err := svc.SyncDatasourceSource(context.Background(), "actor", testOrg, source.ID, "replacement"); err == nil {
		t.Fatal("accepted invalid replacement")
	}
	current, _ := store.GetDatasourceSource(context.Background(), testOrg, source.ID)
	if current.CredentialSecretRef != original || len(producer.jobs) != 0 {
		t.Fatal("invalid replacement mutated source")
	}
	client.branchErr = nil
	if _, err := svc.SyncDatasourceSource(context.Background(), "actor", testOrg, source.ID, "replacement"); err != nil {
		t.Fatal(err)
	}
	current, _ = store.GetDatasourceSource(context.Background(), testOrg, source.ID)
	if len(store.sources) != 1 || current.ID != source.ID || current.BoundaryNodeID != source.BoundaryNodeID || current.CredentialSecretRef == original || len(producer.jobs) != 1 {
		t.Fatal("source identity or recovery failed")
	}
	if _, err := svc.SyncDatasourceSource(context.Background(), "actor", "foreign-org", source.ID, "replacement"); err == nil {
		t.Fatal("replaced another tenant source")
	}
	if !auditHas(audit, business.EventDatasourceCredentialUpdated) {
		t.Fatal("credential replacement was not audited")
	}
}

func TestGitHubCredentialFailureRetryAndAudit(t *testing.T) {
	for _, tc := range []struct {
		code  int
		retry bool
	}{{400, false}, {503, true}, {403, true}} {
		store := newDatasourceFakeStore()
		producer := &recordingProducer{}
		svc, audit := newDatasourceService(store, producer, nil)
		source := addSource(t, svc, business.AddGitHubSourceInput{OrgID: testOrg, Repo: "acme/docs", CollectionLabel: "docs", AccessToken: "old"})
		svc.SetDatasourceConnector(brokenDatasourceCipher{err: vaultStatusError(tc.code)}, producer, "")
		err := svc.NewDatasourceDeliveryJobHandler()(context.Background(), &jobsv1.JobEnvelope{Id: "job", Queue: business.DatasourceDeliveryQueue, Topic: "datasource.github.reconcile", AttemptCount: 2, Attributes: map[string]string{"datasource.source_id": source.ID}})
		var processing *jobs.ProcessingError
		if !errors.As(err, &processing) || processing.Retryable != tc.retry {
			t.Fatalf("status %d: wrong retry policy: %v", tc.code, err)
		}
		if strings.Contains(err.Error(), "provider-body-secret") {
			t.Fatal("secret leaked")
		}
		if !auditHas(audit, business.EventDatasourceSyncFailed) {
			t.Fatal("failure missing from audit")
		}
	}
}
