package business

import (
	"context"
	"fmt"
	"time"

	"github.com/codefly-dev/core/solutionhost"
)

// SolutionDeliveryObserverAuthorizer grants the protected endpoint publisher a
// read of committed presence. It grants no ability to deliver documents.
type SolutionDeliveryObserverAuthorizer interface {
	AuthorizeAppliedObserver(context.Context, string) error
}

// SolutionDeliveryApplied reports a committed generation, never an inbox receipt
// or a desired generation. A publisher must compare every identity field with
// its expected declaration before publishing corresponding endpoints.
type SolutionDeliveryApplied struct {
	Host           solutionhost.HostTarget `json:"host"`
	Binding        string                  `json:"binding"`
	Generation     uint64                  `json:"generation"`
	PresenceDigest string                  `json:"presenceDigest"`
	Release        string                  `json:"release"`
	Removed        bool                    `json:"removed"`
	AppliedAt      time.Time               `json:"appliedAt"`
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
		applied := record.Applied
		// The row's host fields may already describe a withheld desired update.
		// All reported identity comes from the committed APPLIED document.
		document, err := solutionhost.Parse([]byte(applied.Document))
		if err != nil {
			return nil, fmt.Errorf("read applied presence: %w", err)
		}
		digest, err := document.Digest()
		if err != nil {
			return nil, fmt.Errorf("digest applied presence: %w", err)
		}
		if document.Binding != binding || document.Generation != applied.Generation ||
			digest != applied.Digest || document.Removed != applied.Removed ||
			document.Release.Identity() != applied.Release || applied.At.IsZero() {
			return nil, fmt.Errorf("applied presence record is inconsistent")
		}
		return &SolutionDeliveryApplied{
			Host: document.Host, Binding: binding, Generation: document.Generation,
			PresenceDigest: digest, Release: applied.Release, Removed: document.Removed,
			AppliedAt: applied.At,
		}, nil
	}
	return nil, ErrSolutionHostBindingNotDeclared
}
