package business

import "context"

// noopControlPlaneStore satisfies the Store interface by embedding it, so every
// method exists and only the control-plane transaction is implemented: it runs
// fn directly. An embedded nil panics loudly if a test reaches a method it did
// not mean to, which is the behaviour worth having in a test about ordering
// rather than persistence.
type noopControlPlaneStore struct{ Store }

func (noopControlPlaneStore) WithControlPlane(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}
