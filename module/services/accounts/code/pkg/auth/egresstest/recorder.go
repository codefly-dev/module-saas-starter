package egresstest

import (
	"errors"
	"sync"
	"syscall"
)

// ErrStopped ends a recorded dial before it connects, whatever the guard
// decided, so a test of a guard's wiring never sends traffic anywhere.
var ErrStopped = errors.New("egresstest: dial stopped before connect")

// Control is the shape of a net.Dialer Control hook.
type Control = func(network, address string, c syscall.RawConn) error

// Verdict is one dial a Recorder saw: the address the hook was handed and what
// the wrapped guard decided about it.
type Verdict struct {
	Address string
	Err     error
}

// Recorder wraps a guard so a test can prove a client's dial runs it, on the
// address actually being dialed, without letting any connection proceed.
type Recorder struct {
	mu       sync.Mutex
	verdicts []Verdict
}

// Wrap returns a Control hook that asks guard, records the verdict, and then
// stops the dial: a refusal is returned as-is and an admission as ErrStopped.
func (r *Recorder) Wrap(guard Control) Control {
	return func(network, address string, c syscall.RawConn) error {
		err := guard(network, address, c)
		r.mu.Lock()
		r.verdicts = append(r.verdicts, Verdict{Address: address, Err: err})
		r.mu.Unlock()
		if err == nil {
			return ErrStopped
		}
		return err
	}
}

// Take returns the verdicts recorded so far and forgets them.
func (r *Recorder) Take() []Verdict {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.verdicts
	r.verdicts = nil
	return out
}
