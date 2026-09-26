package connector

import (
	"context"
	"errors"
	"io"
)

// Clause 6, the budget half: one provider credential's quota is shared by
// every source that uses it, and a sync a person started is served before
// background syncs. The host holds the meter (a Scheduler); Budgeted puts it
// in front of every operation of a connector.

// Priority says who is waiting on an operation.
type Priority int

const (
	// PriorityBackground is work nobody is waiting on: a periodic reconcile,
	// a webhook delivery. It is the default, and it yields.
	PriorityBackground Priority = iota
	// PriorityInteractive is work a person started or is waiting on.
	PriorityInteractive
)

func (p Priority) String() string {
	if p == PriorityInteractive {
		return "interactive"
	}
	return "background"
}

type priorityKey struct{}

// WithPriority marks the operations run under ctx.
func WithPriority(ctx context.Context, p Priority) context.Context {
	return context.WithValue(ctx, priorityKey{}, p)
}

// PriorityFrom reads the priority ctx carries; unmarked work is background.
func PriorityFrom(ctx context.Context) Priority {
	if p, ok := ctx.Value(priorityKey{}).(Priority); ok {
		return p
	}
	return PriorityBackground
}

// Scheduler meters a credential's operations. Acquire spends one operation of
// src's credential at priority, or refuses with a *RateLimitedError naming
// when to try again; Blocked records a provider's own rate limit, so nothing
// spends the credential before it resets.
type Scheduler interface {
	Acquire(ctx context.Context, src Source, budget Budget, priority Priority) error
	Blocked(ctx context.Context, src Source, limited *RateLimitedError) error
}

// Budgeted is c with every operation metered by s. A provider rate limit c
// reports is recorded against the credential before it is returned.
func Budgeted(c FilesConnector, s Scheduler) FilesConnector {
	return &budgeted{inner: c, scheduler: s}
}

type budgeted struct {
	inner     FilesConnector
	scheduler Scheduler
}

func (b *budgeted) Descriptor() Descriptor { return b.inner.Descriptor() }

func (b *budgeted) acquire(ctx context.Context, src Source) error {
	return b.scheduler.Acquire(ctx, src, b.inner.Descriptor().Budget, PriorityFrom(ctx))
}

// observe records a provider rate limit; the scheduler's own refusals never
// reach it, since they are returned before the provider is called.
func (b *budgeted) observe(ctx context.Context, src Source, err error) error {
	var limited *RateLimitedError
	if errors.As(err, &limited) && !limited.Yielded {
		if recErr := b.scheduler.Blocked(ctx, src, limited); recErr != nil {
			return errors.Join(err, recErr)
		}
	}
	return err
}

func (b *budgeted) Version(ctx context.Context, src Source) (string, error) {
	if err := b.acquire(ctx, src); err != nil {
		return "", err
	}
	v, err := b.inner.Version(ctx, src)
	return v, b.observe(ctx, src, err)
}

func (b *budgeted) Changes(ctx context.Context, src Source, from string) (ChangeSet, error) {
	if err := b.acquire(ctx, src); err != nil {
		return ChangeSet{}, err
	}
	cs, err := b.inner.Changes(ctx, src, from)
	return cs, b.observe(ctx, src, err)
}

func (b *budgeted) FetchFiles(ctx context.Context, src Source, version string, refs []FileRef, visit func(File, io.Reader) error) error {
	if err := b.acquire(ctx, src); err != nil {
		return err
	}
	return b.observe(ctx, src, b.inner.FetchFiles(ctx, src, version, refs, visit))
}
