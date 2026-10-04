package infra

import (
	"context"

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
		return nil, err
	}
	return &business.ReviewedExecutionToken{
		ServiceAccount: reviewed.ServiceAccount,
		Namespace:      reviewed.Namespace,
		PodName:        reviewed.PodName,
		PodUID:         reviewed.PodUID,
	}, nil
}

// RunningContainer reads what a container asked to run and what it runs.
func (c *KubernetesClient) RunningContainer(
	ctx context.Context, namespace, pod, container string,
) (*business.RunningContainerStatus, error) {
	image, err := c.runningContainer(ctx, namespace, pod, container)
	if err != nil {
		return nil, err
	}
	return &business.RunningContainerStatus{
		UID:           image.UID,
		ImageID:       image.ImageID,
		DeclaredImage: image.DeclaredImage,
		Found:         image.Found,
	}, nil
}

var _ business.ExecutionReviewer = (*KubernetesClient)(nil)
