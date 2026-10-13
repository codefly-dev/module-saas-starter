//go:build pure

package business

import (
	"context"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Both complete registry generations refuse this exact token+pod.
// Only combining the old container selection with the new workload owner accepts.
func TestOwnerLookupUsesGrantSnapshot(t *testing.T) {
	oldProcs := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(oldProcs)
	a, err := parseRegistryFixture(`{"alpha":{"tenant":"11111111-1111-4111-8111-111111111111","workload":{"namespace":"acme-prod","service_account":"alpha","container":"worker"}},"beta":{"tenant":"22222222-2222-4222-8222-222222222222","workload":{"namespace":"acme-prod","service_account":"beta","container":"worker"}}}`)
	require.NoError(t, err)
	b, err := parseRegistryFixture(`{"alpha":{"tenant":"33333333-3333-4333-8333-333333333333","workload":{"namespace":"acme-prod","service_account":"beta","container":"new-worker"}},"beta":{"tenant":"22222222-2222-4222-8222-222222222222","workload":{"namespace":"acme-prod","service_account":"other","container":"worker"}}}`)
	require.NoError(t, err)
	reviewed := reviewedPod()
	reviewed.ServiceAccount = "beta"
	service := boundService(&fakeExecutionReviewer{reviewed: reviewed, pod: runningPodWith()}, &fakeExecutionAuthority{digest: approvedBare, incarnation: 1})
	ctx := context.Background()
	id := ModulePrincipalID("alpha")
	service.SetModulePrincipals(a)
	_, err = service.BindExecution(ctx, service.moduleRegistrySnapshot(), id, "beta-token")
	require.ErrorIs(t, err, ErrExecutionIdentityMismatch, "generation A must refuse beta's token")
	snapshot := service.moduleRegistrySnapshot()
	service.SetModulePrincipals(b)
	execution, err := service.BindExecution(ctx, snapshot, id, "beta-token")
	require.ErrorIs(t, err, ErrExecutionIdentityMismatch, "the passed A snapshot owns both the container and the owner lookup")
	require.Nil(t, execution)
	_, err = service.BindExecution(ctx, service.moduleRegistrySnapshot(), id, "beta-token")
	require.ErrorIs(t, err, ErrExecutionUnapprovedContainer, "generation B requires new-worker, not worker")
	stop := make(chan struct{})
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			service.SetModulePrincipals(a)
			service.SetModulePrincipals(b)
		}
	}()
	defer func() { close(stop); writers.Wait() }()
	for i := 0; i < 500000; i++ {
		execution, err := service.BindExecution(ctx, service.moduleRegistrySnapshot(), id, "beta-token")
		if err == nil {
			t.Fatalf("cross-generation authorization at attempt %d: principal=%s SA=%s container=%s; neither registry generation authorizes this execution", i, execution.PrincipalID, execution.ServiceAccount, execution.ContainerName)
		}
	}
}

type replacingExecutionReviewer struct {
	fakeExecutionReviewer
	replace func()
}

func (r *replacingExecutionReviewer) ReviewToken(ctx context.Context, token, audience string) (*ReviewedExecutionToken, error) {
	r.replace()
	return r.fakeExecutionReviewer.ReviewToken(ctx, token, audience)
}
func TestRegistryReplacementDuringReviewCannotAddOperationAuthority(t *testing.T) {
	registry, err := parseRegistryFixture(`{"alpha":{"tenant":"11111111-1111-4111-8111-111111111111","workload":{"namespace":"acme-prod","service_account":"worker","container":"worker"}}}`)
	require.NoError(t, err)
	replacement, err := parseRegistryFixture(`{"alpha":{"tenant":"22222222-2222-4222-8222-222222222222","workload":{"namespace":"acme-prod","service_account":"replacement","container":"worker"},"operation_audiences":{"operate":{"audience":"example-operation","invoke_scopes":[{"resource_kind":"results","actions":["read","write"]}],"lookup_scopes":[{"resource_kind":"results","actions":["read"]}],"headless_scopes":[{"resource_kind":"results","actions":["write"]}]}}}}`)
	require.NoError(t, err)
	service := &Service{}
	reviewer := &replacingExecutionReviewer{fakeExecutionReviewer: fakeExecutionReviewer{reviewed: reviewedPod(), pod: runningPodWith()}}
	service.SetExecutionBinding(reviewer, &fakeExecutionAuthority{digest: approvedBare, incarnation: 1})
	reviewer.replace = func() {}
	service.SetModulePrincipals(registry)
	_, err = service.ModuleAuthorizeOperationContext(context.Background(), "alpha", "old-token", "operate")
	require.ErrorIs(t, err, ErrModuleOperationContextRefused)
	service.SetModulePrincipals(replacement)
	_, err = service.ModuleAuthorizeOperationContext(context.Background(), "alpha", "old-token", "operate")
	require.ErrorIs(t, err, ErrModuleIdentityDenied)
	service.SetModulePrincipals(registry)
	reviewer.replace = func() { service.SetModulePrincipals(replacement) }
	authority, err := service.ModuleAuthorizeOperationContext(context.Background(), "alpha", "old-token", "operate")
	require.ErrorIs(t, err, ErrModuleRegistryStale)
	require.Empty(t, authority)
}

// A commit during TokenReview must not reuse the request memo's entry revision.
func TestModuleMintFencesARevisionCommittedDuringReview(t *testing.T) {
	service := boundService(&fakeExecutionReviewer{reviewed: reviewedPod(), pod: runningPodWith()}, &fakeExecutionAuthority{digest: approvedBare, incarnation: 1})
	registry := service.ModulePrincipals()
	grant := registry["principal-1"]
	registry = ModulePrincipalRegistry{ModulePrincipalID("worker"): grant}
	service.InstallDerivedModulePrincipals(registry, 3, nil)
	envelope := &fakeEnvelopeStore{latest: 3}
	service.SetModuleEnvelopeStore(envelope)
	reviewer := &replacingExecutionReviewer{fakeExecutionReviewer: fakeExecutionReviewer{reviewed: reviewedPod(), pod: runningPodWith()}, replace: func() { envelope.latest = 4 }}
	service.SetExecutionBinding(reviewer, &fakeExecutionAuthority{digest: approvedBare, incarnation: 1})
	authority, err := service.ModuleAuthorizeWorkContext(WithModuleEnvelopeRevisionMemo(context.Background()), "worker", "token")
	require.ErrorIs(t, err, ErrModuleRegistryStale)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Empty(t, authority)
	require.Equal(t, 2, envelope.reads, "the final fence reads past the memo populated at entry")
}
