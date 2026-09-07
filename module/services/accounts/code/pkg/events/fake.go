package events

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// FakeTransport is an in-memory Transport for unit tests. It reproduces the
// durable semantics the conformance suite checks — at-least-once, id dedupe,
// per-partition FIFO, lease lifecycle, dead-letter, replay, and tenant scope —
// without a database, so a consumer written against it behaves the same when
// pointed at PostgresTransport.
type FakeTransport struct {
	mu            sync.Mutex
	subscriptions []Subscription
	leaseDuration time.Duration
	maxAttempts   int

	log      []*EventEnvelope
	queues   map[string][]*fakeDelivery
	byToken  map[string]*fakeDelivery
	enqueued map[string]struct{}
}

type fakeDelivery struct {
	token       string
	envelope    *EventEnvelope
	partition   string
	ordered     bool
	attempts    int
	state       deliveryState
	leaseExpiry time.Time
	availableAt time.Time
}

type deliveryState int

const (
	statePending deliveryState = iota
	stateProcessing
	stateDone
	stateDeadLetter
)

// NewFakeTransport builds an in-memory transport over the given subscriptions.
// leaseDuration bounds each claimed lease.
func NewFakeTransport(subscriptions []Subscription, leaseDuration time.Duration) *FakeTransport {
	return &FakeTransport{
		subscriptions: subscriptions,
		leaseDuration: leaseDuration,
		maxAttempts:   defaultMaxAttempts,
		queues:        map[string][]*fakeDelivery{},
		byToken:       map[string]*fakeDelivery{},
		enqueued:      map[string]struct{}{},
	}
}

var _ Transport = (*FakeTransport)(nil)

const defaultMaxAttempts = 3

func (f *FakeTransport) Publish(_ context.Context, _ TxHandle, e *EventEnvelope) error {
	if e.GetId() == "" || e.GetType() == "" || e.GetSource() == "" {
		return ErrInvalidEnvelope
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, proto.Clone(e).(*EventEnvelope))
	f.fanOut(e)
	return nil
}

func (f *FakeTransport) fanOut(e *EventEnvelope) {
	for _, subscription := range f.subscriptions {
		if !Matches(subscription.TypePattern, e.GetType()) {
			continue
		}
		key := e.GetId() + ":" + subscription.ID
		if _, seen := f.enqueued[key]; seen {
			continue
		}
		f.enqueued[key] = struct{}{}
		f.queues[subscription.Queue] = append(f.queues[subscription.Queue], &fakeDelivery{
			envelope:  proto.Clone(e).(*EventEnvelope),
			partition: e.GetPartitionKey(),
			ordered:   subscription.Delivery == DeliveryOrdered && e.GetPartitionKey() != "",
			state:     statePending,
		})
	}
}

func (f *FakeTransport) Claim(_ context.Context, queue string, max int) ([]Leased, error) {
	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recoverExpired(queue, now)

	blocked := map[string]struct{}{}
	leased := make([]Leased, 0, max)
	for _, delivery := range f.queues[queue] {
		if delivery.ordered {
			if _, isBlocked := blocked[delivery.partition]; isBlocked {
				continue
			}
			if delivery.state == statePending || delivery.state == stateProcessing {
				blocked[delivery.partition] = struct{}{}
			}
		}
		if len(leased) >= max || delivery.state != statePending || now.Before(delivery.availableAt) {
			continue
		}
		delivery.state = stateProcessing
		delivery.token = uuid.NewString()
		delivery.leaseExpiry = now.Add(f.leaseDuration)
		f.byToken[delivery.token] = delivery
		leased = append(leased, Leased{Token: delivery.token, Envelope: proto.Clone(delivery.envelope).(*EventEnvelope)})
	}
	return leased, nil
}

func (f *FakeTransport) recoverExpired(queue string, now time.Time) {
	for _, delivery := range f.queues[queue] {
		if delivery.state == stateProcessing && !now.Before(delivery.leaseExpiry) {
			delete(f.byToken, delivery.token)
			delivery.token = ""
			f.failAttempt(delivery, now)
		}
	}
}

func (f *FakeTransport) Heartbeat(_ context.Context, token string) error {
	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	delivery, ok := f.byToken[token]
	if !ok || delivery.state != stateProcessing || now.After(delivery.leaseExpiry) {
		return ErrLeaseLost
	}
	delivery.leaseExpiry = now.Add(f.leaseDuration)
	return nil
}

func (f *FakeTransport) Ack(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delivery, ok := f.byToken[token]
	if !ok || delivery.state != stateProcessing {
		return ErrLeaseLost
	}
	delivery.state = stateDone
	delete(f.byToken, token)
	return nil
}

func (f *FakeTransport) Nack(_ context.Context, token string, _ error, permanent bool) error {
	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	delivery, ok := f.byToken[token]
	if !ok || delivery.state != stateProcessing {
		return ErrLeaseLost
	}
	delete(f.byToken, token)
	delivery.token = ""
	if permanent {
		delivery.state = stateDeadLetter
		return nil
	}
	f.failAttempt(delivery, now)
	return nil
}

func (f *FakeTransport) failAttempt(delivery *fakeDelivery, now time.Time) {
	delivery.attempts++
	if delivery.attempts >= f.maxAttempts {
		delivery.state = stateDeadLetter
		return
	}
	delivery.state = statePending
	delivery.availableAt = now
}

func (f *FakeTransport) Replay(_ context.Context, sel ReplaySelector) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	replayed := 0
	for _, e := range f.log {
		if !selectorMatches(sel, e) {
			continue
		}
		replayed++
		for _, subscription := range f.subscriptions {
			if !Matches(subscription.TypePattern, e.GetType()) {
				continue
			}
			f.queues[subscription.Queue] = append(f.queues[subscription.Queue], &fakeDelivery{
				envelope:  proto.Clone(e).(*EventEnvelope),
				partition: e.GetPartitionKey(),
				ordered:   subscription.Delivery == DeliveryOrdered && e.GetPartitionKey() != "",
				state:     statePending,
			})
		}
	}
	return replayed, nil
}

func selectorMatches(sel ReplaySelector, e *EventEnvelope) bool {
	if sel.Type != "" && e.GetType() != sel.Type {
		return false
	}
	if sel.TenantID != "" && e.GetTenantId() != sel.TenantID {
		return false
	}
	if !sel.Since.IsZero() && e.GetTime() != nil && e.GetTime().AsTime().Before(sel.Since) {
		return false
	}
	return true
}
