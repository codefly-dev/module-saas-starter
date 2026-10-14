package business

import (
	"context"
	"fmt"
	"time"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/module-saas-starter/module/frontendpayload"
)

// A retained identity grants no installation or API authority.
type SolutionFrontendGeneration struct {
	SolutionHostBindingGeneration
	TargetID string
	Frontend *solutionhost.Frontend
	Release  string
}
type SolutionFrontendHistory interface {
	GetRetainedSolutionFrontend(context.Context, string, string, time.Time) (*SolutionFrontendGeneration, error)
}

// Configure source and retention before reconciliation starts.
func (s *Service) SetFrontendArtifactSource(source frontendpayload.Source) { s.frontendSource = source }
func (s *Service) SetFrontendLazyLoadWindow(window time.Duration)          { s.frontendLazyLoadWindow = window }
func (s *Service) GetRetainedSolutionFrontend(ctx context.Context, binding, version string) (*SolutionFrontendGeneration, error) {
	reader, ok := s.store.(SolutionFrontendHistory)
	if !ok {
		return nil, nil
	}
	window := s.frontendLazyLoadWindow
	if window <= 0 {
		window = 24 * time.Hour
	}
	var generation *SolutionFrontendGeneration
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		generation, err = reader.GetRetainedSolutionFrontend(ctx, binding, version, time.Now().Add(-window))
		return err
	})
	if err != nil || generation == nil {
		return nil, err
	}
	document, err := solutionhost.Parse([]byte(generation.Document))
	if err != nil {
		return nil, fmt.Errorf("read retained frontend presence: %w", err)
	}
	digest, err := document.Digest()
	if err != nil || digest != generation.Digest || document.Binding != binding || document.Generation != generation.Generation || document.Removed || frontendpayload.AssetVersion(binding, generation.TargetID, document.Generation, document.Release.Identity()) != version {
		return nil, fmt.Errorf("retained frontend differs from its applied history")
	}
	if document.Frontend == nil || document.Frontend.Payload == nil {
		return nil, nil
	}
	generation.Frontend = document.Frontend
	generation.Release = document.Release.Identity()
	return generation, nil
}
