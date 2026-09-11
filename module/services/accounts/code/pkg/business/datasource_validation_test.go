package business_test

import (
	"accounts/pkg/business"
	"accounts/pkg/datasource/github"
	"context"
	"errors"
	"strings"
	"testing"
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
			_, err := svc.AddGitHubSource(context.Background(), "actor", business.AddGitHubSourceInput{OrgID: testOrg, Repo: "acme/docs", Branch: "main", CollectionLabel: "docs", AccessToken: "secret-sensitive-token"})
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
