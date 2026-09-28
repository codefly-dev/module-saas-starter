package business

import "context"

// The sync job a hand-off belongs to, carried from the datasource job handler
// to the enqueue sites that stamp it.
//
// Request-scoped, and it travels through several layers that have no other
// reason to know it — the same shape as the connector priority the handler sets
// three lines away. Threading it as a parameter would have added an argument to
// the compiler's whole public call chain, and to every one of its callers, to
// carry a value none of them read.
type syncJobIDKey struct{}

// withSyncJobID carries the durable sync job's id for the work this context
// covers. Set once per delivery, at the handler, so both the reconcile and the
// push topic are covered by one statement rather than by each path remembering.
func withSyncJobID(ctx context.Context, jobID string) context.Context {
	if jobID == "" {
		return ctx
	}
	return context.WithValue(ctx, syncJobIDKey{}, jobID)
}

// syncJobIDFrom reports the sync job this work belongs to, or "" when the work
// did not arrive through the delivery queue — a direct call in a test, or a
// future caller that does not run under a job. An empty value is left off the
// hand-off attributes rather than stamped blank, so a consumer can tell "this
// hand-off names no sync" from "this hand-off names the empty sync".
func syncJobIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(syncJobIDKey{}).(string)
	return id
}

// syncJobAttributes stamps the sync a hand-off belongs to, or nothing at all
// when the work did not arrive through the delivery queue. Nothing rather than
// an empty value: a consumer can then tell a hand-off that names no sync from
// one that names the empty sync.
func syncJobAttributes(ctx context.Context) map[string]string {
	id := syncJobIDFrom(ctx)
	if id == "" {
		return nil
	}
	return map[string]string{attrSyncJobID: id}
}
