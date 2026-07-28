package analytics

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"

	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"

	"google.golang.org/protobuf/proto"
)

var (
	ErrEventConflict          = errors.New("analytics: event id was reused with different content")
	ErrSuppressionUnsupported = errors.New("analytics: destination does not support this suppression target")
)

type Delivery struct {
	Reference string
	Duplicate bool
}

type Sink interface {
	Capture(context.Context, *analyticsv1.ProductEvent) (Delivery, error)
}

type Destination interface {
	Sink
	IdentitySink
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
	CommandID      string
	UserID         string
	OrganizationID string
}

type IdentitySink interface {
	Identify(context.Context, Identity) error
	Alias(context.Context, Alias) error
	Group(context.Context, Group) error
	Suppress(context.Context, Suppression) (Delivery, error)
}

type DeliveryRecord struct {
	JobID             string
	CommandID         string
	Kind              string
	ProviderReference string
	Duplicate         bool
	DeliveredAt       time.Time
}

type DeliveryRecorder interface {
	RecordDelivery(context.Context, DeliveryRecord) error
}

type NoopSink struct{}

func (NoopSink) Capture(
	_ context.Context,
	event *analyticsv1.ProductEvent,
) (Delivery, error) {
	return Delivery{Reference: event.GetEventId()}, nil
}

func (NoopSink) Identify(context.Context, Identity) error { return nil }
func (NoopSink) Alias(context.Context, Alias) error       { return nil }
func (NoopSink) Group(context.Context, Group) error       { return nil }
func (NoopSink) Suppress(_ context.Context, command Suppression) (Delivery, error) {
	return Delivery{Reference: command.CommandID}, nil
}

type MemorySink struct {
	mu              sync.Mutex
	events          []*analyticsv1.ProductEvent
	byID            map[string][sha256.Size]byte
	identity        []Identity
	aliases         []Alias
	groups          []Group
	suppress        []Suppression
	bySuppressionID map[string]Suppression
}

func NewMemorySink() *MemorySink {
	return &MemorySink{
		byID:            make(map[string][sha256.Size]byte),
		bySuppressionID: make(map[string]Suppression),
	}
}

func (s *MemorySink) Capture(
	_ context.Context,
	event *analyticsv1.ProductEvent,
) (Delivery, error) {
	logicalEvent := proto.Clone(event).(*analyticsv1.ProductEvent)
	logicalEvent.ReceivedAt = nil
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(logicalEvent)
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

func (s *MemorySink) Suppress(
	_ context.Context,
	command Suppression,
) (Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.bySuppressionID[command.CommandID]; ok {
		if existing != command {
			return Delivery{}, ErrEventConflict
		}
		return Delivery{Reference: command.CommandID, Duplicate: true}, nil
	}
	s.bySuppressionID[command.CommandID] = command
	s.suppress = append(s.suppress, command)
	return Delivery{Reference: command.CommandID}, nil
}

func (s *MemorySink) Suppressions() []Suppression {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Suppression(nil), s.suppress...)
}

type MemoryDeliveryRecorder struct {
	mu      sync.Mutex
	records []DeliveryRecord
}

func NewMemoryDeliveryRecorder() *MemoryDeliveryRecorder {
	return &MemoryDeliveryRecorder{}
}

func (r *MemoryDeliveryRecorder) RecordDelivery(
	_ context.Context,
	record DeliveryRecord,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record)
	return nil
}

func (r *MemoryDeliveryRecorder) Records() []DeliveryRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]DeliveryRecord(nil), r.records...)
}
