package business

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The serving gate: MayServe, on the request path.
//
// WHY NOT AT STARTUP. The state the gate refuses on OPENS AT RUNTIME. A commit
// that fails after its append landed leaves the log saying a narrowing happened
// and this host not having applied it, and a host that only checked at boot
// would serve the wider authority it still holds locally for exactly as long as
// that gap stays open — which is the window the whole protocol exists for. The
// same is true of the staleness condition: a log reachable at boot and
// unreachable an hour later must stop the host, and only a check on the request
// path can notice.
//
// WHY A REFUSAL AND NOT A DEGRADATION. Both conditions mean the same thing: this
// host does not know that the authority it is about to apply is current. Serving
// a narrower answer is not available — the host cannot compute one without the
// log — so the honest answer is to refuse the request. It is an OUTAGE, not a
// denial: the caller is not unauthorized, the host is unable, so the code is
// Unavailable and never PermissionDenied. A host that answered "forbidden" here
// would tell every caller their authority had been revoked when nothing had been
// revoked at all.

// policyLogServingCacheFor is how long one serving answer is reused.
//
// Not zero: MayServe is a control-plane transaction, and one per request would
// put a cross-tenant round trip in front of every call the host serves — a cost
// the gate cannot justify when the answer changes only when a gap opens or the
// log is reached.
//
// Not longer either, and the bound is about what the cache can HIDE. Two things
// change the answer. A gap this host opens itself does not wait for the TTL at
// all: WithPolicyLoggedNarrowing invalidates the cache the instant it records an
// append, so the very next request re-reads. A gap another replica opened
// arrives here through reconciliation, whose interval is longer than this
// window, so the TTL adds nothing to that delay. One second is therefore the
// window in which the cache can hide nothing that is not already pending.
const policyLogServingCacheFor = time.Second

// policyLogServingCache is the serving gate's memory of its last answer.
//
// ONLY A PERMISSION IS REMEMBERED, never a refusal, and the asymmetry is the
// point. A remembered permission can be wrong for at most the window below, and
// the two things that could make it wrong are both accounted for: a gap this
// host opens itself raises the dirty flag at the instant the append is recorded,
// and a gap another replica opened arrives through reconciliation, whose
// interval is longer than the window. A remembered REFUSAL has no such bound —
// a gap closed by an operator, or by whatever re-ran the narrowing, is closed in
// the database and nothing tells this process — so a cached refusal would keep
// the host shut after the reason had gone. Re-reading while refusing costs
// nothing that matters: a host in that state is serving no traffic to spend the
// read on.
type policyLogServingCache struct {
	mu    sync.Mutex
	at    time.Time
	state *PolicyLogServingState
	dirty atomic.Bool
}

// invalidate discards the cached answer. Called by WithPolicyLoggedNarrowing
// around the commit, so the gap it may have just opened — and the gap it may
// have just closed — are both read fresh.
func (c *policyLogServingCache) invalidate() {
	if c == nil {
		return
	}
	c.dirty.Store(true)
}

// ErrNotServing reports that the policy log forbids this host from serving.
//
// It wraps the specific condition, so a caller that cares can tell an
// unreconciled gap from an unreachable log; a caller that does not gets one
// sentinel to test.
var ErrNotServing = errors.New("this host is not serving")

// RequireServing refuses when the policy log says this host must not serve.
//
// Called on the REQUEST PATH, by every transport's authorization interceptor,
// and deliberately before the handler rather than inside it: a request that
// reaches a handler has already been authorized against local state the host
// may no longer be entitled to use.
//
// A failure to EVALUATE the gate is also a refusal. If the control-plane read
// errors, the host does not know whether it holds a gap, and "I could not check"
// must not be served as "there is nothing to find" — that is the one substitution
// that turns a fail-closed protocol into a fail-open one.
func (s *Service) RequireServing(ctx context.Context) error {
	ok, state, err := s.mayServeCached(ctx)
	if err != nil {
		return status.Error(codes.Unavailable, fmt.Sprintf(
			"this host cannot determine whether its authority is current: %v", err))
	}
	if ok {
		return nil
	}
	return status.Error(codes.Unavailable, notServingReason(state))
}

// notServingReason says which condition closed the gate, without naming the
// subjects of the gaps: a refusal is served to an unauthenticated caller as
// readily as to an administrator, and the identities whose authority was
// narrowed are not a detail that belongs in it. The operation ids are in the
// local relation for an operator who can read it.
func notServingReason(state *PolicyLogServingState) string {
	switch {
	case state == nil:
		return "this host is not serving: the policy log state is unknown"
	case state.Unreachable && len(state.Gaps) > 0:
		return fmt.Sprintf("this host is not serving: the policy log has not been reached, "+
			"and %d logged narrowing(s) are not applied here", len(state.Gaps))
	case state.Unreachable:
		return "this host is not serving: the policy log has not been reached within the staleness " +
			"window, so this host does not know whether its authority is current"
	case len(state.Gaps) > 0:
		return fmt.Sprintf("this host is not serving: %d narrowing(s) the policy log has witnessed "+
			"are not applied here", len(state.Gaps))
	default:
		return "this host is not serving"
	}
}

// mayServeCached is MayServe with the bounded reuse described above. The cached
// answer is dropped when this host's own append raised the dirty flag, and the
// flag is cleared only after a fresh evaluation has replaced the answer — so a
// concurrent request cannot clear it and then serve the stale decision.
func (s *Service) mayServeCached(ctx context.Context) (bool, *PolicyLogServingState, error) {
	cache := &s.policyServing
	now := s.policyNow()

	cache.mu.Lock()
	// cache.state is set only when the answer was "may serve", so a hit is a
	// remembered permission by construction and a refusal is never reused.
	fresh := cache.state != nil && !cache.dirty.Load() &&
		now.Sub(cache.at) >= 0 && now.Sub(cache.at) < policyLogServingCacheFor
	if fresh {
		state := cache.state
		cache.mu.Unlock()
		return true, state, nil
	}
	cache.mu.Unlock()

	ok, state, err := s.MayServe(ctx)
	if err != nil {
		return false, nil, err
	}

	cache.mu.Lock()
	if ok {
		cache.at, cache.state = now, state
		cache.dirty.Store(false)
	} else {
		cache.state = nil
	}
	cache.mu.Unlock()
	return ok, state, nil
}

// ReconcilePolicyLogEvery runs reconciliation on an interval until ctx is done.
//
// On an interval and not only at startup, for the same reason the gate is on the
// request path: the two facts it maintains both change while the host runs. It
// refreshes `reached_at`, which is what keeps the staleness window open — so a
// host whose reconciliation loop dies stops serving, which is the correct
// consequence of no longer knowing the log's state. And it records entries other
// replicas appended, so a narrowing one replica logged and failed to apply
// closes every replica's gate rather than only its own.
//
// A failed pass is not fatal and is not retried faster. The cursor is not
// advanced on failure, so the staleness window closes on its own and the gate
// does the refusing; a tighter retry loop would only hammer an unreachable log.
func (s *Service) ReconcilePolicyLogEvery(ctx context.Context, every time.Duration, onError func(error)) {
	if s.policyLog == nil || s.policyLogStore == nil {
		// Nothing is reconcilable, and the loop would be a ticker that reports
		// an empty state forever.
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if _, err := s.ReconcilePolicyLog(ctx); err != nil && onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// PolicyLogReconcileInterval is how often the loop above runs.
//
// Shorter than PolicyLogStaleAfter, and that relationship is the point rather
// than the number: a host must get several chances to reach the log before the
// staleness window closes, or a single slow pass would take it out of service.
const PolicyLogReconcileInterval = 15 * time.Second
