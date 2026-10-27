package main

// Read-only client and replica cache for the durable declared solution registry.
// The gateway refreshes periodically and on cache misses, and brokers the
// projection to the frontend over its existing accounts connection.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/module-saas-starter/module/frontendpayload"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

const (
	listSolutionRegistrationsMethod = "/saas.accounts.v1.SolutionRegistryService/ListSolutionRegistrations"
)

const (
	// solutionRegistryCallTimeout bounds one registry read.
	solutionRegistryCallTimeout = 10 * time.Second
	// solutionReconcileInterval is the convergence bound: after a write on any
	// replica, every other replica reflects it within one interval plus the
	// round trip.
	solutionReconcileInterval = 10 * time.Second
	// solutionRefreshFloor rate-limits the on-demand refresh a cache miss
	// triggers, so a flood of requests for an unregistered id cannot turn into
	// a flood of registry reads. It is short because it is also the worst-case
	// convergence delay for a request that arrives at a replica the
	// declaration has not reached yet.
	solutionRefreshFloor = 250 * time.Millisecond
	// solutionRegistryLease is the revocation bound: how long a snapshot keeps
	// authorising routes after the read that produced it. An applied tombstone
	// reaches every replica within the reconcile interval while the registry
	// answers; while it does not, a snapshot older than this stops authorising
	// solution and module routes rather than serving a withdrawn presence
	// indefinitely. The lease is the bound every reader of the registry is
	// documented against (SOLUTION_REGISTRATION.md).
	solutionRegistryLease = 120 * time.Second
)

// solutionRegistrySnapshot joins only matching applied generations. The compact
// registration has no endpoint field; the host-binding read carries Core's
// complete applied module manifest. No new wire schema duplicates that contract.
type solutionRegistrySnapshot struct {
	*accountsv1.ListSolutionRegistrationsResponse
	// routeEndpoints is, per declared alias — a solution's or a module's — the
	// endpoints its applied presence declares for the service that runs it
	// (appliedRouteEndpoints), with the addresses the manifest carries.
	routeEndpoints map[string][]moduleEndpoint
}

// solutionRegistryClient is the registry surface used by the cache and handlers.
type solutionRegistryClient interface {
	List(ctx context.Context, req *accountsv1.ListSolutionRegistrationsRequest) (*solutionRegistrySnapshot, error)
}

// accountsSolutionRegistry invokes the registry by method name over the
// existing accounts connection, presenting the gateway's cluster-internal
// credential. There is no vendored client stub for SolutionRegistryService, so
// the methods are called by name against generated message types shared with
// accounts.
type accountsSolutionRegistry struct {
	conn          *grpc.ClientConn
	internalToken string
}

func (c *accountsSolutionRegistry) invoke(ctx context.Context, method string, req, resp any) error {
	ctx, cancel := context.WithTimeout(ctx, solutionRegistryCallTimeout)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "x-codefly-internal-token", c.internalToken)
	return c.conn.Invoke(ctx, method, req, resp)
}

func (c *accountsSolutionRegistry) List(ctx context.Context, req *accountsv1.ListSolutionRegistrationsRequest) (*solutionRegistrySnapshot, error) {
	resp := &accountsv1.ListSolutionRegistrationsResponse{}
	if err := c.invoke(ctx, listSolutionRegistrationsMethod, req, resp); err != nil {
		return nil, err
	}
	bindings := &accountsv1.ListSolutionHostBindingsResponse{}
	if err := c.invoke(ctx, "/saas.accounts.v1.SolutionRegistryService/ListSolutionHostBindings", &accountsv1.ListSolutionHostBindingsRequest{}, bindings); err != nil {
		return nil, err
	}
	out := &solutionRegistrySnapshot{ListSolutionRegistrationsResponse: resp, routeEndpoints: make(map[string][]moduleEndpoint)}
	for _, record := range resp.GetRegistrations() {
		for _, binding := range bindings.GetBindings() {
			// These reads may straddle reconciliation. Never attach a successor's
			// endpoints to its predecessor's record, even within one generation.
			if !proto.Equal(record, binding.GetRegistration()) {
				continue
			}
			if endpoints := appliedRouteEndpoints(binding); len(endpoints) > 0 {
				out.routeEndpoints[record.GetSolutionId()] = endpoints
			}
		}
	}
	return out, nil
}

// Historical assets are read by identity, never broadcast in the route snapshot.
type solutionFrontendReader interface {
	GetRetainedFrontend(context.Context, string, string) (*accountsv1.SolutionFrontendGeneration, error)
}

func (c *accountsSolutionRegistry) GetRetainedFrontend(ctx context.Context, binding, version string) (*accountsv1.SolutionFrontendGeneration, error) {
	response := &accountsv1.GetRetainedSolutionFrontendResponse{}
	err := c.invoke(ctx, "/saas.accounts.v1.SolutionRegistryService/GetRetainedSolutionFrontend", &accountsv1.GetRetainedSolutionFrontendRequest{BindingId: binding, AssetVersion: version}, response)
	return response.GetFrontend(), err
}

// solutionResolution is why a solution is or is not routable right now. The
// distinctions are the ones an operator needs: never registered, registered but
// not yet whole, declared and unavailable, and "we cannot tell".
type solutionResolution int

const (
	solutionRoutable solutionResolution = iota
	solutionUnregistered
	solutionNotActive
	solutionRegistryUnavailable
	// solutionWrongKind: the alias IS declared, and as the other kind — or with a
	// kind this gateway cannot read. Each surface answers it as its own 403.
	//
	// Distinct from solutionUnregistered because the two are different facts: one
	// is an unexposed path, the other a verdict about a record that is here. And
	// distinct from solutionNotActive because no deployment arriving changes it.
	solutionWrongKind
	// solutionRegistryStale: a snapshot loaded, and it is older than the
	// revocation lease because every read since has failed. Distinct from
	// solutionRegistryUnavailable — a record IS held, and an operator reading
	// the 503 needs to know the registry was reachable once and is not now —
	// and answered the same way: nothing is authorised on it.
	solutionRegistryStale
)

// errSolutionRegistryUnconfigured is what a registry call returns when the
// gateway has no accounts connection. It cannot happen in a deployed gateway —
// main always wires one — and exists so a misconfigured process fails loudly
// instead of silently serving an empty registry.
var errSolutionRegistryUnconfigured = errors.New("solution registry client not configured")

// solutionRegistryCache is the gateway's replica-local view of the registry.
type solutionRegistryCache struct {
	client         solutionRegistryClient
	frontendSource frontendpayload.Source
	frontends      map[string]map[string]*preparedFrontend
	preparing      chan struct{}
	serving        chan struct{}
	bundles        map[string]*frontendpayload.Bundle // guarded by preparing
	bundleOrder    []string
	// now is the cache refresh clock. Tests move it; production leaves
	// it at time.Now.
	now func() time.Time

	// resolveEndpoint answers where an endpoint lives. Production reads the
	// address the applied manifest carries; the registry columns hold none.
	resolveEndpoint func(context.Context, moduleEndpoint) (string, error)

	mu      sync.RWMutex
	records map[string]*accountsv1.SolutionRegistration
	// upstreams is each declared alias's resolved address, taken on the same
	// refresh as the record it routes, so a lookup never pairs one snapshot's
	// record with another's address.
	upstreams map[string]*url.URL
	// routeRefusals remembers, per alias, the last refusal reason logged.
	routeRefusals sync.Map
	revision      int64
	loaded        bool
	// loadedAt is when the snapshot held was READ. The revocation lease is
	// measured from it: a failed read leaves the snapshot where it was, and a
	// snapshot the registry has not confirmed within the lease authorises
	// nothing.
	loadedAt time.Time
	// attemptedAt is when a read was last ATTEMPTED, successfully or not. The
	// refresh floor is measured from it rather than from the last SUCCESS: a
	// failed read leaves the snapshot's age where it was, so measuring from that
	// made every miss during an outage issue its own full-registry read — the
	// flood the floor exists to prevent, arriving exactly when the authority is
	// least able to serve it.
	attemptedAt time.Time
	lastErr     error
	refreshing  sync.Mutex
}

func newSolutionRegistryCache(client solutionRegistryClient) *solutionRegistryCache {
	return &solutionRegistryCache{
		client:          client,
		preparing:       make(chan struct{}, 1),
		serving:         make(chan struct{}, 8),
		bundles:         map[string]*frontendpayload.Bundle{},
		now:             time.Now,
		resolveEndpoint: moduleEndpointResolver,
		records:         map[string]*accountsv1.SolutionRegistration{},
	}
}

// refresh replaces the whole snapshot from authoritative state, tombstones
// included. Carrying the tombstone is what lets this replica tell a solution
// that was removed from one that never registered: routing treats both as gone,
// and the operator projection preserves that distinction.
//
// A failure leaves the previous snapshot in place: a registry outage must not
// empty every replica's routing table. It does age it, and past the lease the
// snapshot stops authorising routes (see lookup).
func (c *solutionRegistryCache) refresh(ctx context.Context) error {
	c.refreshing.Lock()
	defer c.refreshing.Unlock()
	return c.load(ctx)
}

// refreshIfStale is the cache-miss path. It re-checks staleness *under* the
// lock: callers that queued behind an in-flight read are already going to see
// its result, so issuing a fresh registry read for each of them turns a burst
// of misses into a serialized queue of full-registry reads, each request
// waiting out every one before it.
//
// When the floor suppresses the read it reports the outcome of the most recent
// one rather than nil. The suppressed read is the read the caller was going to
// decide on, and a caller cannot tell "I did not need to ask" from "the last
// answer to that question was an error" — so returning nil handed a burst of
// misses arriving inside the floor a clean bill of health drawn from a read that
// had failed, and the first miss a 503 while the next ones answered 502.
func (c *solutionRegistryCache) refreshIfStale(ctx context.Context) error {
	c.refreshing.Lock()
	defer c.refreshing.Unlock()
	if !c.staleEnoughToRefresh() {
		return c.lastLoadErr()
	}
	return c.load(ctx)
}

// lastLoadErr is the outcome of the most recent registry read: nil once one has
// succeeded, the failure while the authority is unreachable.
func (c *solutionRegistryCache) lastLoadErr() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastErr
}

// load performs the read itself. The caller holds c.refreshing.
func (c *solutionRegistryCache) load(ctx context.Context) error {
	if c.client == nil {
		return errSolutionRegistryUnconfigured
	}
	readAt := c.now()
	ctx, cancel := context.WithTimeout(ctx, solutionRegistryCallTimeout)
	defer cancel()
	resp, err := c.client.List(ctx, &accountsv1.ListSolutionRegistrationsRequest{IncludeTombstoned: true})
	// Resolved BEFORE the lock: resolution may read the workspace from disk,
	// and every lookup waits on this lock.
	var upstreams map[string]*url.URL
	var frontends map[string]map[string]*preparedFrontend
	if err == nil {
		upstreams = c.resolveRouteUpstreams(ctx, resp.routeEndpoints)
		frontends = c.prepareFrontends(ctx, resp)
		if c.now().Sub(readAt) > solutionRegistryLease {
			err = context.DeadlineExceeded
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attemptedAt = c.now()
	if err != nil {
		c.lastErr = err
		return err
	}
	records := make(map[string]*accountsv1.SolutionRegistration, len(resp.GetRegistrations()))
	for _, record := range resp.GetRegistrations() {
		records[record.GetSolutionId()] = record
	}
	c.records = records
	c.upstreams = upstreams
	c.frontends = frontends
	c.revision = resp.GetRegistryRevision()
	c.loaded = true
	// Acquisition must never renew the age of the authority read preceding it.
	c.loadedAt = readAt
	c.lastErr = nil
	return nil
}

// reconcile keeps this replica converged until ctx is done. It refreshes once
// immediately so a restarted gateway rebuilds its routing table from durable
// state before it has served much traffic, then on the interval.
func (c *solutionRegistryCache) reconcile(ctx context.Context) {
	if err := c.refresh(ctx); err != nil && ctx.Err() == nil {
		log.Printf("solution registry: initial reconcile failed: %v", err)
	}
	ticker := time.NewTicker(solutionReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.refresh(ctx); err != nil && ctx.Err() == nil {
				log.Printf("solution registry: reconcile failed: %v", err)
			}
		}
	}
}

// resolveRouteUpstreams resolves each declared alias — a solution's
// /solutions/<alias> or a module's /v1/<alias> — to the one address this
// gateway forwards it to: of the HTTP-family endpoints its applied manifest
// declares — the CLI writes exactly the endpoints the host reaches a composed
// workload through, each with its address. An address the upstream policy
// refuses is not a candidate
// (it is logged, and another endpoint may still answer); none leaves the alias
// unrouted; several are refused as ambiguous rather than chosen between. Each
// alias's reason is logged once, and again only after it changes.
func (c *solutionRegistryCache) resolveRouteUpstreams(ctx context.Context, endpoints map[string][]moduleEndpoint) map[string]*url.URL {
	upstreams := make(map[string]*url.URL, len(endpoints))
	for alias, candidates := range endpoints {
		var resolved []*url.URL
		var named, refused []string
		for _, endpoint := range candidates {
			address, err := c.resolveEndpoint(ctx, endpoint)
			if err != nil {
				continue
			}
			upstream, ok := routableUpstream(address)
			if !ok {
				refused = append(refused, endpoint.String())
				continue
			}
			resolved = append(resolved, upstream)
			named = append(named, endpoint.String())
		}
		switch {
		case len(resolved) == 1:
			upstreams[alias] = resolved[0]
			c.noteRouteRouted(alias)
		case len(resolved) == 0 && len(refused) > 0:
			c.noteRouteRefusal(alias, fmt.Sprintf("its endpoints resolve only to addresses the upstream policy refuses (%v)", refused))
		case len(resolved) == 0:
			c.noteRouteRefusal(alias, "its applied manifest gives no address for any HTTP endpoint it declares")
		default:
			c.noteRouteRefusal(alias, fmt.Sprintf("its applied manifest declares several HTTP endpoints (%v); which one the route forwards to has no single answer", named))
		}
	}
	return upstreams
}

// noteRouteRefusal logs why a declared alias is not routed, once per alias
// until the reason changes or the alias is routed again.
func (c *solutionRegistryCache) noteRouteRefusal(alias, reason string) {
	if previous, seen := c.routeRefusals.Swap(alias, reason); seen && previous == reason {
		return
	}
	log.Printf("solution registry: %q is not routed: %s", alias, reason)
}

// noteRouteRouted forgets an alias's last refusal, so a later one is logged.
func (c *solutionRegistryCache) noteRouteRouted(alias string) {
	if _, seen := c.routeRefusals.LoadAndDelete(alias); seen {
		log.Printf("solution registry: %q is routed again", alias)
	}
}

// String spells the endpoint as Codefly references one: module/service/name::api.
func (e moduleEndpoint) String() string {
	return e.Module + "/" + e.Service + "/" + e.Name + "::" + e.API
}

// moduleEndpointResolver is the resolver every cache starts with. A test binary
// replaces it with one standing in for the composition's carriers.
var moduleEndpointResolver = manifestEndpointAddress

// manifestEndpointAddress is where the applied manifest says the endpoint
// lives.
func manifestEndpointAddress(_ context.Context, endpoint moduleEndpoint) (string, error) {
	if endpoint.Address == "" {
		return "", fmt.Errorf("the applied manifest gives %s no address", endpoint)
	}
	return endpoint.Address, nil
}

// routableUpstream holds a resolved address to the upstream URL policy: http or
// https, a host, and nothing but an origin, on a host the guard admits. It is
// the one policy for every address the composition resolves, solution and
// module alike.
func routableUpstream(raw string) (*url.URL, bool) {
	upstream, err := url.Parse(raw)
	if err != nil || upstream.Host == "" ||
		(upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.User != nil ||
		(upstream.Path != "" && upstream.Path != "/") || upstream.RawQuery != "" || upstream.ForceQuery || upstream.Fragment != "" ||
		isDisallowedRegisteredUpstreamHost(upstream.Hostname()) {
		return nil, false
	}
	return &url.URL{Scheme: upstream.Scheme, Host: upstream.Host}, true
}

// lookup reads the cached record without touching the network. stale reports a
// snapshot past the revocation lease, which holds records and authorises none
// of them.
func (c *solutionRegistryCache) lookup(id string) (record *accountsv1.SolutionRegistration, upstream *url.URL, found, loaded, stale bool) {
	read := c.lookupSnapshot(id)
	return read.record, read.upstream, read.found, read.loaded, read.stale
}

func (c *solutionRegistryCache) lookupSnapshot(id string) solutionLookup {
	c.mu.RLock()
	defer c.mu.RUnlock()
	record, found := c.records[id]
	return solutionLookup{record: record, upstream: c.upstreams[id], frontends: c.frontends[id], found: found, loaded: c.loaded, stale: c.loaded && c.now().Sub(c.loadedAt) > solutionRegistryLease}
}

func (c *solutionRegistryCache) staleEnoughToRefresh() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.loaded || c.now().Sub(c.attemptedAt) >= solutionRefreshFloor
}

// solutionLookup is one read of this replica's view of one alias, with the fact
// a verdict must never be derived without: whether the registry could actually
// be consulted about it.
type solutionLookup struct {
	record    *accountsv1.SolutionRegistration
	frontends map[string]*preparedFrontend
	// upstream is the resolved address of the alias, from the same snapshot as
	// record.
	upstream *url.URL
	found    bool
	// loaded is false until a snapshot has ever loaded on this replica.
	loaded bool
	// stale is set when the snapshot is older than the revocation lease and
	// the read that would have renewed it failed: what it holds is no longer
	// the registry's answer.
	stale bool
	// undecidable is set when this alias had no conclusive record in the
	// snapshot AND the on-demand read that would have settled it failed. The
	// lookup then rests on absence of evidence, which is not evidence of
	// absence.
	undecidable bool
}

// conclusive reports whether a held record settles the question on its own,
// with no registry read.
//
// A live record and a TOMBSTONE both do. The tombstone is the one that is easy
// to get wrong: it is positive, durable evidence that an operator removed the
// registration, carried in every snapshot for exactly that reason — so a failed
// refresh must not convert a deliberate removal into an outage, which would
// answer 503 forever for something that is permanently gone.
//
// A record that is declared and not yet active is NOT settled: a solution whose
// presence is applied without its frontend block, or whose address the
// composition has not handed this gateway, should still refresh in case the
// generation that completes it is about to arrive.
func (c *solutionRegistryCache) conclusive(record *accountsv1.SolutionRegistration, found bool) bool {
	if !found {
		return false
	}
	return record.GetTombstonedAt() != nil || solutionRecordActive(record)
}

// lookupFresh reads the snapshot, issuing at most one on-demand registry read
// (floor-limited) when what it holds does not settle the question — so a
// solution whose declaration was reconciled by another replica a moment ago is reachable
// here without waiting out the reconcile interval — or when the snapshot is
// past the lease, so a replica that lost the registry keeps asking for it.
//
// The read's failure is CARRIED rather than logged and dropped. It used to be
// dropped, and the empty lookup that followed was then reported as a fact: the
// proxy answered "solution not registered" and admission answered "your
// organization did not install this" because accounts was down. An authority
// that cannot be asked must never read as an answer from it.
func (c *solutionRegistryCache) lookupFresh(ctx context.Context, id string) solutionLookup {
	read := c.lookupSnapshot(id)
	if !read.stale && c.conclusive(read.record, read.found) {
		return read
	}
	refreshErr := c.refreshIfStale(ctx)
	if refreshErr != nil {
		log.Printf("solution registry: on-demand refresh for %q failed: %v", id, refreshErr)
	}
	read = c.lookupSnapshot(id)
	if read.stale {
		log.Printf("solution registry: the snapshot is past the %s lease and the registry cannot be read; no route is authorised on it", solutionRegistryLease)
	}
	read.undecidable = refreshErr != nil && !c.conclusive(read.record, read.found)
	return read
}

// solutionRouting is ONE resolution of one route alias: the identity admission
// is decided on, and the address traffic is forwarded to, read from the SAME
// record in the SAME lookup.
//
// It is one value rather than two entry points because two reads of mutable
// state are two different answers. The proxy used to take the upstream from
// `resolve` and then, further down the same request, take the target from
// `resolveTarget` — and the snapshot moves between them: a declaration is withdrawn, the
// reconcile loop lands, an on-demand read replaces the record. A replacement
// binding arriving in that window was ADMITTED on its own installation and the
// viewer's bearer was then forwarded to the PREDECESSOR's address, which no
// organisation had consented to reach. Separating the two was argued for as a
// safeguard — "admission needs the identity before any upstream is chosen" —
// and ordering was never the problem: the two reads disagreeing was.
//
// So the resolution is taken once and carried. TargetID is empty for a record
// nothing declared; that is deliberately not an error here, because the caller
// that must refuse it is the one holding the viewer, and it refuses it as a
// verdict rather than as a missing route.
type solutionRouting struct {
	Alias                       string
	AssetVersion                string
	FrontendRequiresPreparation bool
	Frontends                   map[string]*preparedFrontend
	// TargetID is the immutable solution target this alias resolves to, empty
	// for a registration no declaration opened a target for.
	TargetID string
	// BindingID is the declared binding this alias resolves to. It is carried on
	// the SAME resolution as TargetID and Upstream, for the same reason they are:
	// the audience a presented capability is checked against is derived from it
	// (`solution:<binding-id>`), and a second read could judge a token against the
	// predecessor's audience while traffic went to the replacement's address.
	BindingID string
	// Upstream is where the Codefly SDK resolved the alias's endpoint, from
	// the same snapshot as the record, normalised to scheme+host.
	Upstream *url.URL
}

// GetTargetID is the resolved target, or empty on a nil routing. A nil routing
// cannot reach admission — the caller's switch on the resolution returns first —
// and answering empty rather than panicking keeps that a refusal if it ever
// does.
func (r *solutionRouting) GetTargetID() string {
	if r == nil {
		return ""
	}
	return r.TargetID
}

// resolveRouting answers where to proxy a /solutions/<id>/* request AND which
// solution identity that destination is, or why neither can be answered. A read
// whose snapshot does not settle the question triggers at most one on-demand
// registry read (floor-limited), so a solution whose declaration another
// replica reconciled a moment ago is reachable here without waiting out the reconcile
// interval.
func (c *solutionRegistryCache) resolveRouting(ctx context.Context, id string) (*solutionRouting, solutionResolution) {
	return c.resolveRoutingOfKind(ctx, id, solutionSurface)
}

// routingSurface is which surface is asking. It exists so the kind check is a
// parameter of one resolution rather than a second copy of it.
type routingSurface int

const (
	solutionSurface routingSurface = iota
	frontendSurface
	retainedFrontendSurface
)

func (c *solutionRegistryCache) resolveRoutingOfKind(
	ctx context.Context, id string, surface routingSurface,
) (*solutionRouting, solutionResolution) {
	read := c.lookupFresh(ctx, id)
	record, found := read.record, read.found
	if !read.loaded || read.undecidable {
		return nil, solutionRegistryUnavailable
	}
	// Past the lease, a held record is not the registry's answer any more: a
	// tombstone applied during the outage would be invisible here, so nothing
	// is authorised on the snapshot, tombstones included (a withdrawal already
	// held is answered as the registry would have, which is the one reading
	// that cannot be wrong).
	if read.stale && (!found || record.GetTombstonedAt() == nil) {
		return nil, solutionRegistryStale
	}
	// A tombstoned record is in the snapshot so declaration reconciliation and diagnostics
	// can see it, but it routes exactly like an id that never registered. This is
	// also how a declared presence stops being routed: carrier deletion is not
	// revocation, the applied tombstone is, and it reaches this read within the
	// lease.
	if !found || record.GetTombstonedAt() != nil {
		return nil, solutionUnregistered
	}
	// The declaration positively identifies the routing surface. Both kinds
	// require organization consent; an unspecified kind is served by neither.
	switch surface {
	case frontendSurface, retainedFrontendSurface:
		// A shared module can contribute a page. Only its public asset surface
		// uses this path; its APIs keep the module route and audience checks.
		if (!declaredSolutionKind(record) && !declaredModuleKind(record)) ||
			(surface != retainedFrontendSurface && record.GetFrontend().GetManifest() == "") {
			return nil, solutionWrongKind
		}
	default:
		if !declaredSolutionKind(record) {
			return nil, solutionWrongKind
		}
	}
	if !solutionRecordActive(record) {
		return nil, solutionNotActive
	}
	// The address is RESOLVED, never recorded: the Codefly SDK answers for the
	// endpoint the applied presence declares, for a solution exactly as for a
	// module. No address means the composition handed this gateway no carrier
	// for it, which is "not serving" and not "not registered".
	if read.upstream == nil && surface != retainedFrontendSurface && !(surface == frontendSurface && frontendRequiresPreparation(record)) {
		return nil, solutionNotActive
	}
	return &solutionRouting{
		Alias:                       record.GetSolutionId(),
		AssetVersion:                frontendAssetVersion(record),
		FrontendRequiresPreparation: frontendRequiresPreparation(record),
		Frontends:                   read.frontends,
		TargetID:                    record.GetDeclared().GetTargetId(),
		BindingID:                   record.GetDeclared().GetBindingId(),
		Upstream:                    read.upstream,
	}, solutionRoutable
}

// solutionRecordActive reads the availability accounts derived from applied
// presence. Frontend availability and installation readiness are separate facts.
// A record without a resolved address is not routable on this replica.
func solutionRecordActive(record *accountsv1.SolutionRegistration) bool {
	return record != nil && record.GetTombstonedAt() == nil &&
		record.GetStatus() == accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_ACTIVE
}

// snapshot returns the cached records ordered by id, along with the registry
// revision they were read at and whether a snapshot has ever loaded.
func (c *solutionRegistryCache) snapshot() ([]*accountsv1.SolutionRegistration, int64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	records := make([]*accountsv1.SolutionRegistration, 0, len(c.records))
	for _, record := range c.records {
		records = append(records, record)
	}
	sortSolutionRecords(records)
	return records, c.revision, c.loaded
}

func sortSolutionRecords(records []*accountsv1.SolutionRegistration) {
	for i := 1; i < len(records); i++ {
		for j := i; j > 0 && records[j].GetSolutionId() < records[j-1].GetSolutionId(); j-- {
			records[j], records[j-1] = records[j-1], records[j]
		}
	}
}

// solutionRegistryStatusLabel renders a record's status for a diagnostic
// response. It carries no address, manifest, or credential — only the state
// name an operator needs to tell these cases apart.
func solutionRegistryStatusLabel(record *accountsv1.SolutionRegistration) string {
	switch record.GetStatus() {
	case accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_ACTIVE:
		return "active"
	case accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_PENDING:
		return "pending"
	case accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_TOMBSTONED:
		return "tombstoned"
	default:
		return fmt.Sprintf("unknown(%d)", record.GetStatus())
	}
}
