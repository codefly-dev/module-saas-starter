package infra

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/business"
)

// KubernetesClient as the execution reviewer the business layer asks.
//
// A thin adapter rather than business depending on this package's types: the
// business layer owns what an execution decision needs, and the shape of
// Kubernetes' API is this package's problem. Written out so the mapping between
// the two is visible in one place — in particular that the SPEC's image and the
// STATUS's imageID stay separate all the way across the boundary, because
// collapsing them here would defeat the three-type separation no matter how
// carefully the business layer compares.

// ReviewToken authenticates a projected service-account token.
func (c *KubernetesClient) ReviewToken(
	ctx context.Context, token, audience string,
) (*business.ReviewedExecutionToken, error) {
	reviewed, err := c.reviewToken(ctx, token, audience)
	if err != nil {
		// A review that RAN and refused is a verdict on the token; the business
		// layer keeps it apart from a review that could not run, and it can only
		// do that from a sentinel it owns.
		if errors.Is(err, ErrTokenRejected) {
			return nil, fmt.Errorf("%w: %w", business.ErrExecutionTokenRejected, err)
		}
		return nil, err
	}
	// The lifetime is read only once the API server has authenticated the
	// token, so it is the cluster's own statement, not the caller's. A module
	// mint needs it (the lifetime bounds a copied token's use); a delivery
	// carrier's review does not, which is why it is read here and not in
	// reviewToken.
	issuedAt, expiresAt, err := authenticatedTokenLifetime(token)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", business.ErrExecutionTokenRejected, err)
	}
	return &business.ReviewedExecutionToken{
		ServiceAccount: reviewed.ServiceAccount,
		Namespace:      reviewed.Namespace,
		PodName:        reviewed.PodName,
		PodUID:         reviewed.PodUID,
		IssuedAt:       issuedAt,
		ExpiresAt:      expiresAt,
	}, nil
}

// ReadPod reads the pod a reviewed token named, with every container in it.
func (c *KubernetesClient) ReadPod(ctx context.Context, namespace, pod string) (*business.PodExecution, error) {
	return c.readPod(ctx, namespace, pod)
}

var _ business.ExecutionReviewer = (*KubernetesClient)(nil)
