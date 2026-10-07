package adapters

import (
	"context"
	"sync/atomic"
)

// The policy log's serving gate, as the transports reach it.
//
// WHY A REGISTERED GATE RATHER THAN A SERVICE REFERENCE. The authorization
// interceptors are built by generated code (grpc_gen.go, connect_gen.go) which
// hands them a minter accessor and nothing else, and generated code is not
// hand-editable. The host registers the gate at boot the same way it registers
// its non-proto HTTP routes, and the interceptors call it through this file.
//
// WHY AN UNSET GATE ADMITS. Registration is boot wiring: a deployment with no
// policy log has nothing unreconciled to honour, which is exactly what
// Service.MayServe answers for a host with no log. The refusal that a host
// without a log owes is on the NARROWING path — it cannot reduce authority —
// and WithPolicyLoggedNarrowing makes it there, not here. Refusing every
// request instead would take a host out of service for a protocol it is not
// participating in.
var policyLogServingGate atomic.Pointer[func(context.Context) error]

// RegisterPolicyLogServingGate installs the host's serving gate.
//
// Boot wiring, called once by the host before it listens. It takes the function
// rather than the service so this package keeps no handle on the service beyond
// the one question it asks.
func RegisterPolicyLogServingGate(gate func(ctx context.Context) error) {
	if gate == nil {
		policyLogServingGate.Store(nil)
		return
	}
	policyLogServingGate.Store(&gate)
}

// enforcePolicyLogServing refuses a call when the policy log says this host must
// not serve.
//
// It runs on every unary and streaming call of both transports, AFTER
// authorization rather than before it: an unauthenticated caller learns nothing
// about this host's reconciliation state, and the refusal reaches only callers
// that would otherwise have been served.
func enforcePolicyLogServing(ctx context.Context) error {
	gate := policyLogServingGate.Load()
	if gate == nil {
		return nil
	}
	return (*gate)(ctx)
}
