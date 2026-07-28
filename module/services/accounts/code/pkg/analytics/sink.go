package analytics

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"

	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"

	"google.golang.org/protobuf/proto"
)

var ErrEventConflict = errors.New("analytics: event id was reused with different content")

type Delivery struct {
	Reference string
	Duplicate bool
}

type Sink interface {
	Capture(context.Context, *analyticsv1.ProductEvent) (Delivery, error)
}

type Identity struct {
	DistinctID     string
	OrganizationID string
	Properties     map[string]any
}

type Alias struct {
	PreviousID string
	DistinctID string
}

type Group struct {
	OrganizationID string
	Properties     map[string]any
}

type Suppression struct {
	UserID         string
	OrganizationID string
}

type IdentitySink interface {
	Identify(context.Context, Identity) error
	Alias(context.Context, Alias) error
	Group(context.Context, Group) error
	Suppress(context.Context, Suppression) error
}

type NoopSink struct{}

func (NoopSink) Capture(context.Context, *analyticsv1.ProductEvent) (Delivery, error) {
	return Delivery{}, nil
}

func (NoopSink) Identify(context.Context, Identity) error    { return nil }
func (NoopSink) Alias(context.Context, Alias) error          { return nil }
func (NoopSink) Group(context.Context, Group) error          { return nil }
func (NoopSink) Suppress(context.Context, Suppression) error { return nil }

type MemorySink struct {
	mu       sync.Mutex
	events   []*analyticsv1.ProductEvent
	byID     map[string][sha256.Size]byte
	identity []Identity
	aliases  []Alias
	groups   []Group
	suppress []Suppression
}

func NewMemorySink() *MemorySink {
	return &MemorySink{byID: make(map[string][sha256.Size]byte)}
}

func (s *MemorySink) Capture(
	_ context.Context,
	event *analyticsv1.ProductEvent,
) (Delivery, error) {
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(event)
	if err != nil {
		return Delivery{}, err
	}
	fingerprint := sha256.Sum256(encoded)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.byID[event.GetEventId()]; ok {
		if existing != fingerprint {
			return Delivery{}, ErrEventConflict
		}
		return Delivery{Reference: event.GetEventId(), Duplicate: true}, nil
	}
	s.byID[event.GetEventId()] = fingerprint
	s.events = append(s.events, proto.Clone(event).(*analyticsv1.ProductEvent))
	return Delivery{Reference: event.GetEventId()}, nil
}

func (s *MemorySink) Events() []*analyticsv1.ProductEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*analyticsv1.ProductEvent, 0, len(s.events))
	for _, event := range s.events {
		out = append(out, proto.Clone(event).(*analyticsv1.ProductEvent))
	}
	return out
}

func (s *MemorySink) Identify(_ context.Context, command Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.identity = append(s.identity, command)
	return nil
}

func (s *MemorySink) Alias(_ context.Context, command Alias) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aliases = append(s.aliases, command)
	return nil
}

func (s *MemorySink) Group(_ context.Context, command Group) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups = append(s.groups, command)
	return nil
}

func (s *MemorySink) Suppress(_ context.Context, command Suppression) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.suppress = append(s.suppress, command)
	return nil
}
