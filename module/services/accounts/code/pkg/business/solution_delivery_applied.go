package business

import (
	"context"
	"fmt"
	"time"

	"github.com/codefly-dev/core/solutionhost"
)

// SolutionDeliveryObserverAuthorizer grants the protected applied-state
// observer a read of committed manifests. It grants no ability to deliver.
type SolutionDeliveryObserverAuthorizer interface {
	AuthorizeAppliedObserver(context.Context, string) error
}

// SolutionDeliveryApplied reports a committed generation, never an inbox receipt
// or a desired generation, read from the binding's applied manifest. A reader
// compares every identity field with the manifest it delivered: a receipt says
// the carrier reached the inbox; only this says the module is served.
type SolutionDeliveryApplied struct {
	Host       solutionhost.HostTarget `json:"host"`
	Binding    string                  `json:"binding"`
	Generation uint64                  `json:"generation"`
	// Digest is the applied manifest's digest (solutionhost.ModuleManifest.Digest).
	Digest    string    `json:"digest"`
	Release   string    `json:"release"`
	Removed   bool      `json:"removed"`
	AppliedAt time.Time `json:"appliedAt"`
	// Pending is a newer generation this host received and withheld, with the
	// host's own reason: "delivered" is not "applied", and the reader is told
	// which.
	Pending *SolutionDeliveryPending `json:"pending,omitempty"`
}

// SolutionDeliveryPending is a received generation this host has not applied
// and why.
type SolutionDeliveryPending struct {
	Generation uint64 `json:"generation"`
	Reason     string `json:"reason"`
}

// ReadSolutionDeliveryApplied authenticates before reading any durable binding.
// Desired-only and unknown bindings both have no APPLIED state to report.
func (s *Service) ReadSolutionDeliveryApplied(ctx context.Context, binding, credential string) (*SolutionDeliveryApplied, error) {
	observer, ok := s.deliveryCarrier.(SolutionDeliveryObserverAuthorizer)
	if !ok {
		return nil, fmt.Errorf("applied observer is not configured")
	}
	if err := observer.AuthorizeAppliedObserver(ctx, credential); err != nil {
		return nil, err
	}
	records, err := s.ListSolutionHostBindings(ctx)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record == nil || record.BindingID != binding || record.Applied == nil {
			continue
		}
		manifest, err := record.AppliedManifest()
		if err != nil {
			return nil, fmt.Errorf("read the applied manifest: %w", err)
		}
		digest, err := manifest.Digest()
		if err != nil || digest != record.Applied.Digest ||
			manifest.Release.Identity() != record.Applied.Release || record.Applied.At.IsZero() {
			return nil, fmt.Errorf("the applied record of binding %q is inconsistent with its manifest", binding)
		}
		applied := &SolutionDeliveryApplied{
			Host: manifest.Host, Binding: binding, Generation: manifest.Generation,
			Digest: digest, Release: record.Applied.Release, Removed: manifest.Removed,
			AppliedAt: record.Applied.At,
		}
		if pending := record.PendingGeneration(); pending != 0 && record.PendingReason != "" {
			applied.Pending = &SolutionDeliveryPending{Generation: pending, Reason: record.PendingReason}
		}
		return applied, nil
	}
	return nil, ErrSolutionHostBindingNotDeclared
}
