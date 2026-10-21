package business

import (
	"context"
	"fmt"
	"reflect"
)

// ModuleEnvelopeHistoryStore reads the existing append-only history through an
// exact committed revision. Revision numbers need not be contiguous.
type ModuleEnvelopeHistoryStore interface {
	ModuleEnvelopeHistory(context.Context, uint64) ([]*ModuleEnvelopeRecord, error)
}

// principalEnvelopeGenerations gives each present principal the revision of its
// last ceiling change. It includes intermediate removals, even when no replica
// reconciled them, and is deterministic across restarts.
func principalEnvelopeGenerations(records []*ModuleEnvelopeRecord, through uint64) (map[string]uint64, error) {
	generations := map[string]uint64{}
	previous := map[string]ModuleEnvelopePrincipal{}
	var revision uint64
	for _, record := range records {
		if record == nil || record.Revision <= revision || record.Revision > through {
			return nil, fmt.Errorf("module envelope history is not ordered through the snapshot")
		}
		envelope, err := ParseModuleEnvelope(record.Document)
		if err != nil || envelope.Revision != record.Revision {
			return nil, fmt.Errorf("module envelope history cannot establish revision %d", record.Revision)
		}
		for principal, ceiling := range envelope.Principals {
			old, existed := previous[principal]
			if !existed || !reflect.DeepEqual(old, ceiling) {
				generations[principal] = record.Revision
			}
		}
		for principal := range previous {
			if _, exists := envelope.Principals[principal]; !exists {
				delete(generations, principal)
			}
		}
		previous, revision = envelope.Principals, record.Revision
	}
	if through == 0 || revision != through {
		return nil, fmt.Errorf("module envelope history does not reach snapshot revision %d", through)
	}
	return generations, nil
}

func (s *Service) moduleEnvelopeGenerations(ctx context.Context, revision uint64) (map[string]uint64, error) {
	history, ok := s.moduleEnvelopes.(ModuleEnvelopeHistoryStore)
	if !ok {
		return nil, nil
	} // Legacy test stores confer no installed-use authority.
	var records []*ModuleEnvelopeRecord
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		records, err = history.ModuleEnvelopeHistory(ctx, revision)
		return err
	})
	if err != nil {
		return nil, err
	}
	return principalEnvelopeGenerations(records, revision)
}
