package main

// The gateway's view of the registered-client registry (handbook track 0016).
//
// A registered client is a first-party caller the host mints tokens for: its
// own web frontend, an add-in, a CLI. accounts owns the registry — it is
// operator configuration there, not tenant data — and this file is the
// gateway's replica-local snapshot of it, rebuilt on a timer and on demand
// after a miss, the same way gateway_solution_registry.go tracks the solution
// registry.
//
// The gateway reads one fact from it: the exact browser origins a client speaks
// from. That is what lets a request be served cross-origin on its bearer alone,
// with no shared secret and no per-client proxy (see gateway_cors.go). accounts
// canonicalizes and validates the origins when it loads them, so they are
// compared to a browser's Origin header by string equality.

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const listRegisteredClientsMethod = "/saas.accounts.v1.ClientRegistryService/ListRegisteredClients"

const (
	// clientRegistryCallTimeout bounds one registry round trip. Nothing blocks
	// a person on it — a miss denies cross-origin access for that request and
	// the next one retries — so it only has to stop a stalled accounts from
	// pinning the caller's connection.
	clientRegistryCallTimeout = 10 * time.Second
	// clientReconcileInterval is the convergence bound. The registry is process
	// configuration on the accounts side, so it changes only when accounts
	// restarts; this cadence exists to pick that up, not to track a write feed.
	clientReconcileInterval = 60 * time.Second
	// clientRefreshFloor rate-limits the on-demand refresh an unknown origin
	// triggers. Without it, requests from origins that are not registered —
	// which any host page's own cross-site POST supplies, and which an attacker
	// can supply at will — would each turn into a registry read.
	clientRefreshFloor = 250 * time.Millisecond
)

// clientRegistryClient is the registry surface the gateway consumes. The
// interface exists so the cache and the CORS pass can be exercised without an
// accounts process.
type clientRegistryClient interface {
	List(ctx context.Context, req *accountsv1.ListRegisteredClientsRequest) (*accountsv1.ListRegisteredClientsResponse, error)
}

// accountsClientRegistry invokes the registry by method name over the existing
// accounts connection, presenting the gateway's cluster-internal credential.
// There is no vendored client stub for ClientRegistryService, so the method is
// called by name against generated message types shared with accounts.
type accountsClientRegistry struct {
	conn          *grpc.ClientConn
	internalToken string
}

func (c *accountsClientRegistry) List(
	ctx context.Context, req *accountsv1.ListRegisteredClientsRequest,
) (*accountsv1.ListRegisteredClientsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, clientRegistryCallTimeout)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "x-codefly-internal-token", c.internalToken)
	resp := &accountsv1.ListRegisteredClientsResponse{}
	return resp, c.conn.Invoke(ctx, listRegisteredClientsMethod, req, resp)
}

// errClientRegistryUnconfigured is what a registry call returns when the
// gateway has no accounts connection. A gateway built without one grants no
// cross-origin access at all, which is the behaviour that predates the
// registry.
var errClientRegistryUnconfigured = errors.New("client registry client not configured")

// clientRegistryCache is the gateway's replica-local view of the registry,
// indexed the way the CORS pass reads it: origin → the clients that registered
// it. Two clients may name the same origin (a developer running both against
// localhost), so the index holds a set rather than a single id.
type clientRegistryCache struct {
	client clientRegistryClient
	// now is the clock the refresh floor reads. Tests move it; production
	// leaves it at time.Now.
	now func() time.Time

	mu            sync.RWMutex
	originClients map[string]map[string]struct{}
	loaded        bool
	loadedAt      time.Time
	refreshing    sync.Mutex
}

func newClientRegistryCache(client clientRegistryClient) *clientRegistryCache {
	return &clientRegistryCache{
		client:        client,
		now:           time.Now,
		originClients: map[string]map[string]struct{}{},
	}
}

// load performs the read itself. The caller holds c.refreshing.
//
// A failure leaves the previous snapshot in place: an accounts outage must not
// withdraw cross-origin access from every registered client at once.
func (c *clientRegistryCache) load(ctx context.Context) error {
	if c.client == nil {
		return errClientRegistryUnconfigured
	}
	resp, err := c.client.List(ctx, &accountsv1.ListRegisteredClientsRequest{})
	if err != nil {
		return err
	}
	index := map[string]map[string]struct{}{}
	for _, registered := range resp.GetClients() {
		id := registered.GetClientId()
		if id == "" {
			continue
		}
		for _, origin := range registered.GetOrigins() {
			if origin == "" {
				continue
			}
			if index[origin] == nil {
				index[origin] = map[string]struct{}{}
			}
			index[origin][id] = struct{}{}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.originClients = index
	c.loaded = true
	c.loadedAt = c.now()
	return nil
}

func (c *clientRegistryCache) refresh(ctx context.Context) error {
	c.refreshing.Lock()
	defer c.refreshing.Unlock()
	return c.load(ctx)
}

// refreshIfStale is the cache-miss path. It re-checks staleness *under* the
// lock so a burst of misses collapses onto one registry read instead of
// queueing one per request.
func (c *clientRegistryCache) refreshIfStale(ctx context.Context) error {
	c.refreshing.Lock()
	defer c.refreshing.Unlock()
	c.mu.RLock()
	fresh := c.loaded && c.now().Sub(c.loadedAt) < clientRefreshFloor
	c.mu.RUnlock()
	if fresh {
		return nil
	}
	return c.load(ctx)
}

// reconcile keeps this replica converged until ctx is done.
func (c *clientRegistryCache) reconcile(ctx context.Context) {
	if err := c.refresh(ctx); err != nil && ctx.Err() == nil {
		log.Printf("client registry: initial reconcile failed: %v", err)
	}
	ticker := time.NewTicker(clientReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.refresh(ctx); err != nil && ctx.Err() == nil {
				log.Printf("client registry: reconcile failed: %v", err)
			}
		}
	}
}

func (c *clientRegistryCache) lookup(origin string) (map[string]struct{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	clients, ok := c.originClients[origin]
	return clients, ok
}

// clientsFor returns the ids registered against an exact browser origin. A miss
// triggers at most one floor-limited refresh, so a client that accounts learned
// about after this replica last reconciled is reachable without waiting out the
// interval.
//
// Every uncertainty answers "no client": an unreadable registry, a registry
// that has never loaded, and an origin nobody registered are indistinguishable
// to a caller, and all three leave the gateway behaving as it did before any
// client was registered.
func (c *clientRegistryCache) clientsFor(ctx context.Context, origin string) map[string]struct{} {
	if origin == "" {
		return nil
	}
	clients, found := c.lookup(origin)
	if found {
		return clients
	}
	if err := c.refreshIfStale(ctx); err != nil {
		log.Printf("client registry: on-demand refresh for origin %q failed: %v", origin, err)
		return nil
	}
	clients, _ = c.lookup(origin)
	return clients
}

// originRegistered reports whether any registered client speaks from origin.
// It is what a preflight is answered from: a preflight carries no credential,
// so the most it can establish is that the origin belongs to the registry.
func (c *clientRegistryCache) originRegistered(ctx context.Context, origin string) bool {
	return len(c.clientsFor(ctx, origin)) > 0
}

// registers reports whether clientID — the `azp` of the bearer that arrived
// with the request — declared this origin. This is the binding that makes a
// stolen token useless from anywhere but the client it was issued to.
func (c *clientRegistryCache) registers(ctx context.Context, clientID, origin string) bool {
	if clientID == "" {
		return false
	}
	_, ok := c.clientsFor(ctx, origin)[clientID]
	return ok
}
