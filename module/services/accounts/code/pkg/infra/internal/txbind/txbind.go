// Package txbind binds a store transaction to a context together with the
// authority it runs with. It is internal to pkg/infra, so the Go toolchain
// refuses to build an import of it from anywhere else: only the code that
// opens a transaction can say what that transaction is. Everything else —
// pkg/business, pkg/auth/pg — reads the transaction back through the
// read-only pkg/infra/storetx.
//
// The authority is recorded because the transaction alone does not say what it
// runs with. A request transaction runs as app_tenant with the org and user
// settings it bound itself, a control-plane transaction has assumed
// app_control_plane, and a worker transaction runs as that worker's role. A
// helper that joins an ambient transaction instead of opening its own must know
// which of these it is joining: joining a control-plane transaction on behalf of
// a tenant identity would run that identity's work across every tenant, and
// joining a request transaction bound for another org or user would run it with
// that org's or user's row visibility.
package txbind

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Pool names the pool a store transaction was opened on, and so the database
// authority it runs with. A single-credential tooling store opens both kinds on
// one pool; its transactions are named by the authority they assume.
type Pool uint8

const (
	// Request is a transaction on the request pool: app_tenant, with the org
	// and user settings it bound for itself, or none.
	Request Pool = iota + 1
	// ControlPlane is a transaction that assumed app_control_plane on the
	// control-plane pool.
	ControlPlane
	// Worker is a transaction on a background worker pool, running as that
	// worker's role.
	Worker
)

func (p Pool) String() string {
	switch p {
	case Request:
		return "request"
	case ControlPlane:
		return "control-plane"
	case Worker:
		return "worker"
	default:
		return "unknown"
	}
}

// Binding is a store transaction and the authority it runs with.
type Binding struct {
	Tx   pgx.Tx
	Pool Pool
	// OrgID and UserID are the request scope a request transaction bound for
	// itself — app.current_org_id and app.current_user_id, set transaction-
	// locally — with "" for a half it cleared or never set. A control-plane or
	// worker transaction binds no request scope, so both are "".
	OrgID  string
	UserID string
}

// key is unexported, and this package internal, so nothing outside pkg/infra
// can write a binding.
type key struct{}

// BindRequest returns ctx carrying tx as a request transaction whose request
// scope is orgID and userID, either possibly empty. The caller states the scope
// the transaction actually carries: the values it bound with set_config, or
// both empty for a transaction on a fresh request connection that bound none —
// the pool's release reset guarantees such a connection carries neither.
func BindRequest(ctx context.Context, tx pgx.Tx, orgID, userID string) context.Context {
	return context.WithValue(ctx, key{}, Binding{Tx: tx, Pool: Request, OrgID: orgID, UserID: userID})
}

// BindControlPlane returns ctx carrying tx as a transaction that has assumed
// app_control_plane.
func BindControlPlane(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, key{}, Binding{Tx: tx, Pool: ControlPlane})
}

// BindWorker returns ctx carrying tx as a transaction on a worker pool.
func BindWorker(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, key{}, Binding{Tx: tx, Pool: Worker})
}

// Lookup returns the store transaction ctx carries with the authority it runs
// with, and false when ctx carries none.
func Lookup(ctx context.Context) (Binding, bool) {
	b, ok := ctx.Value(key{}).(Binding)
	if !ok || b.Tx == nil {
		return Binding{}, false
	}
	return b, true
}
