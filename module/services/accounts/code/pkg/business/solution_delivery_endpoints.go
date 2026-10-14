package business

import (
	"context"
	"errors"
	"fmt"

	"github.com/codefly-dev/core/solutionhost"
)

// Endpoint values share the signed inbox, not the registry's identity tables.
// Core owns their encoding and their join to the complete applied declaration.
func (s *Service) receiveDeliveredEndpoints(ctx context.Context, signed *solutionhost.Signed) (*receivedSolutionDelivery, error) {
	delivered, err := solutionhost.VerifyDeliveredEndpoints(ctx, signed, s.deliveryVerifier)
	if err != nil {
		return nil, classifySolutionDeliveryVerification(err)
	}
	identity, err := delivered.Identity()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
	}
	var presence *solutionhost.SolutionHostBinding
	err = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		records, err := s.store.ListSolutionHostBindings(ctx)
		if err != nil {
			return err
		}
		for _, record := range records {
			if record.BindingID != identity.Binding {
				continue
			}
			presence, err = s.joinEndpointDelivery(delivered, record)
			if errors.Is(err, solutionhost.ErrEndpointsUnapplied) {
				return ErrSolutionDeliveryNotApplied
			}
			if err != nil {
				return fmt.Errorf("%w: %w", ErrSolutionDeliveryInvalid, err)
			}
			return nil
		}
		return ErrSolutionDeliveryNotApplied
	})
	if err != nil {
		return nil, err
	}
	return &receivedSolutionDelivery{
		documentID: identity.Key(), generation: identity.Generation,
		ownershipDomain: identity.OwnershipDomain, signer: delivered.DeliveredBy(),
		payload: delivered.Payload(), carrierNamespaces: declaredNamespaces(presence),
	}, nil
}

func (s *Service) joinEndpointDelivery(delivered *solutionhost.DeliveredEndpoints, record *SolutionHostBindingRecord) (*solutionhost.SolutionHostBinding, error) {
	applied, ok := record.AppliedState()
	if !ok {
		return nil, solutionhost.ErrEndpointsUnapplied
	}
	host := solutionhost.Host{Coordinate: s.deliveryCoordinate, Applied: []solutionhost.Applied{applied}}
	if _, err := host.AdmitEndpoints(delivered); err != nil {
		return nil, err
	}
	presence, err := solutionhost.Parse([]byte(record.Applied.Document))
	if err != nil {
		return nil, err
	}
	// The owner checks the complete document and endpoint subset; Accounts
	// never interprets carriers or chooses an address from the result.
	if _, err := solutionhost.ParseEndpointDelivery(delivered.Payload(), presence); err != nil {
		return nil, err
	}
	return presence, nil
}

// attachAppliedEndpointDeliveries runs inside the same control-plane read as
// the binding projection. A desired-only successor never replaces these bytes.
func (s *Service) attachAppliedEndpointDeliveries(ctx context.Context, records []*SolutionHostBindingRecord) error {
	reader, ok := s.deliveryStore.(SolutionDeliveryReader)
	if !ok {
		return nil // No delivery source means no route, never a boot fallback.
	}
	for _, record := range records {
		if record == nil || record.Applied == nil || record.Applied.Removed {
			continue
		}
		record.Applied.EndpointDelivery = nil
		presence, err := solutionhost.Parse([]byte(record.Applied.Document))
		if err != nil {
			return fmt.Errorf("read applied endpoint presence: %w", err)
		}
		identity := solutionhost.EndpointDeliveryIdentity{Host: presence.Host, Binding: presence.Binding}
		stored, err := reader.ReadDeliveredDocument(ctx, SolutionDeliveryEndpoints, identity.Key(), record.Applied.Generation)
		if err != nil {
			return err
		}
		if stored == nil {
			continue
		}
		delivered, err := solutionhost.VerifyDeliveredEndpoints(ctx, &solutionhost.Signed{Document: stored.Payload, Envelope: stored.Bundle}, s.deliveryVerifier)
		if err != nil {
			return fmt.Errorf("verify applied endpoint delivery: %w", err)
		}
		if !s.signerMaySpeakFor(delivered.DeliveredBy(), record.Applied.Domain) {
			return ErrSolutionDeliveryDomainNotGranted
		}
		if _, err := s.joinEndpointDelivery(delivered, record); err != nil {
			return fmt.Errorf("join applied endpoint delivery: %w", err)
		}
		record.Applied.EndpointDelivery = delivered.Payload()
	}
	return nil
}
