package infra

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/workloadidentity"
	"github.com/codefly-dev/core/workloadidentity/native"
)

// NativeWorkloadIdentity is the host's workload identity under a native
// `codefly run`: the run's launcher signs each process it started — and the
// delivery carrier and applied observer it acts as — a credential of core's
// workloadidentity/native mechanism, and this host verifies it against the
// run's anchor (CODEFLY_NATIVE_IDENTITY_ANCHOR), the way a cell's host reviews
// a projected token. Nothing imitates a cluster: the launcher names the
// declared workload, and that workload is the identity.
//
// It answers the three questions the Kubernetes client answers in a cell:
// who handed over a manifest (the `delivery` workload of the manifest's
// module), who reads applied state (core's AppliedObserver), and which
// workload a minting process is. A native run's namespace is its module: the
// host runs the native rule (business.NativeModuleNamespaceRule).
type NativeWorkloadIdentity struct {
	attester *native.Attester

	// running holds the facts of each attested instance, by module and
	// instance, so the pod read that follows a review answers about the same
	// credential.
	mu      sync.Mutex
	running map[string]*workloadidentity.Facts
}

// NewNativeWorkloadIdentity wraps the run's attester.
func NewNativeWorkloadIdentity(attester *native.Attester) *NativeWorkloadIdentity {
	return &NativeWorkloadIdentity{attester: attester, running: map[string]*workloadidentity.Facts{}}
}

func (n *NativeWorkloadIdentity) attest(ctx context.Context, token, audience string) (*workloadidentity.Attestation, error) {
	if n == nil || n.attester == nil {
		return nil, fmt.Errorf("%w: this host has no native anchor", ErrKubernetesUnavailable)
	}
	if token == "" {
		return nil, fmt.Errorf("%w: no credential was presented", ErrCarrierUnauthenticated)
	}
	attested, err := n.attester.Attest(ctx, token, audience)
	switch {
	case errors.Is(err, workloadidentity.ErrRejected):
		return nil, fmt.Errorf("%w: %w", ErrCarrierUnauthenticated, err)
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrKubernetesUnavailable, err)
	}
	return attested, nil
}

// AuthorizeCarrier requires the credential to name the `delivery` workload of
// module (a native namespace is its module).
func (n *NativeWorkloadIdentity) AuthorizeCarrier(ctx context.Context, credential, module string) error {
	attested, err := n.attest(ctx, bearerToken(credential), deliveryTokenAudience)
	if err != nil {
		return err
	}
	if attested.Workload.Module != module || attested.Workload.Service != workloadidentity.DeliveryService {
		return fmt.Errorf("%w: the manifest is delivered by %s/%s, and the carrier is %s",
			ErrCarrierNotAuthorized, module, workloadidentity.DeliveryService, attested.Workload.Unique())
	}
	return nil
}

// AuthorizeAppliedObserver requires core's applied observer.
func (n *NativeWorkloadIdentity) AuthorizeAppliedObserver(ctx context.Context, credential string) error {
	attested, err := n.attest(ctx, bearerToken(credential), deliveryTokenAudience)
	if err != nil {
		return err
	}
	observer := workloadidentity.AppliedObserver
	if attested.Workload.Module != observer.Module || attested.Workload.Service != observer.Service {
		return fmt.Errorf("%w: caller %s is not the applied-state observer", ErrCarrierNotAuthorized, attested.Workload.Unique())
	}
	return nil
}

// NativeDevelopment is true: no mesh presents a native process.
func (*NativeWorkloadIdentity) NativeDevelopment() bool { return true }

// ReviewToken attests a minting process's credential: its module is the
// namespace, its service the account, its instance the pod, and the
// credential's own id the incarnation.
func (n *NativeWorkloadIdentity) ReviewToken(ctx context.Context, token, audience string) (*business.ReviewedExecutionToken, error) {
	attested, err := n.attest(ctx, token, audience)
	if errors.Is(err, ErrCarrierUnauthenticated) {
		return nil, fmt.Errorf("%w: %w", business.ErrExecutionTokenRejected, err)
	}
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	n.running[attested.Workload.Module+"/"+attested.Workload.Instance] = attested.Facts
	n.mu.Unlock()
	return &business.ReviewedExecutionToken{
		ServiceAccount: attested.Workload.Service, Namespace: attested.Workload.Module,
		PodName: attested.Workload.Instance, PodUID: attested.Facts.InstanceUID,
		IssuedAt: attested.Credential.IssuedAt, ExpiresAt: attested.Credential.ExpiresAt,
	}, nil
}

// ReadPod answers what the launcher vouched for about the instance it attested.
func (n *NativeWorkloadIdentity) ReadPod(_ context.Context, namespace, pod string) (*business.PodExecution, error) {
	n.mu.Lock()
	facts, attested := n.running[namespace+"/"+pod]
	n.mu.Unlock()
	if !attested || facts == nil {
		return nil, fmt.Errorf("%w: native instance %s/%s was not attested", ErrKubernetesUnavailable, namespace, pod)
	}
	read := &business.PodExecution{UID: facts.InstanceUID, Phase: facts.Phase, Terminating: facts.Terminating}
	for _, container := range facts.Containers {
		read.Containers = append(read.Containers, business.ContainerExecution{
			Name: container.Name, Kind: business.ContainerKind(container.Kind), State: business.ContainerState(container.State),
			Restartable: container.Restartable,
		})
	}
	return read, nil
}

var (
	_ business.SolutionDeliveryCarrierAuthorizer  = (*NativeWorkloadIdentity)(nil)
	_ business.SolutionDeliveryObserverAuthorizer = (*NativeWorkloadIdentity)(nil)
	_ business.ExecutionReviewer                  = (*NativeWorkloadIdentity)(nil)
)
