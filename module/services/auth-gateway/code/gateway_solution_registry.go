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

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
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
)

// solutionRegistryClient is the registry surface the gateway consumes. The
// interface exists so the cache and the HTTP handlers can be exercised without
// an accounts process.
type solutionRegistryClient interface {
	List(ctx context.Context, req *accountsv1.ListSolutionRegistrationsRequest) (*accountsv1.ListSolutionRegistrationsResponse, error)
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

func (c *accountsSolutionRegistry) List(ctx context.Context, req *accountsv1.ListSolutionRegistrationsRequest) (*accountsv1.ListSolutionRegistrationsResponse, error) {
	resp := &accountsv1.ListSolutionRegistrationsResponse{}
	return resp, c.invoke(ctx, listSolutionRegistrationsMethod, req, resp)
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
)

// errSolutionRegistryUnconfigured is what a registry call returns when the
// gateway has no accounts connection. It cannot happen in a deployed gateway —
// main always wires one — and exists so a misconfigured process fails loudly
// instead of silently serving an empty registry.
var errSolutionRegistryUnconfigured = errors.New("solution registry client not configured")

// solutionRegistryCache is the gateway's replica-local view of the registry.
type solutionRegistryCache struct {
	client solutionRegistryClient
	// now is the cache refresh clock. Tests move it; production leaves
	// it at time.Now.
	now func() time.Time

	mu       sync.RWMutex
	records  map[string]*accountsv1.SolutionRegistration
	revision int64
	loaded   bool
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
		client:  client,
		now:     time.Now,
		records: map[string]*accountsv1.SolutionRegistration{},
	}
}

// refresh replaces the whole snapshot from authoritative state, tombstones
// included. Carrying the tombstone is what lets this replica tell a solution
// that was removed from one that never registered: routing treats both as gone,
// and the operator projection preserves that distinction.
//
// A failure leaves the previous snapshot in place: a registry outage must not
// empty every replica's routing table.
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
	resp, err := c.client.List(ctx, &accountsv1.ListSolutionRegistrationsRequest{IncludeTombstoned: true})
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
	c.revision = resp.GetRegistryRevision()
	c.loaded = true
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

// lookup reads the cached record without touching the network.
func (c *solutionRegistryCache) lookup(id string) (*accountsv1.SolutionRegistration, bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	record, ok := c.records[id]
	return record, ok, c.loaded
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
	record *accountsv1.SolutionRegistration
	found  bool
	// loaded is false until a snapshot has ever loaded on this replica.
	loaded bool
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
// The surface is carried in because what counts as "serving, so stop asking" is
// per-surface: a record with a backend half and no frontend is settled for a
// module and NOT settled for a solution, which should still refresh in case the
// frontend half is about to arrive.
func (c *solutionRegistryCache) conclusive(
	record *accountsv1.SolutionRegistration, found bool, surface routingSurface,
) bool {
	if !found {
		return false
	}
	return record.GetTombstonedAt() != nil || solutionRecordActive(record, surface)
}

// lookupFresh reads the snapshot, issuing at most one on-demand registry read
// (floor-limited) when what it holds does not settle the question — so a
// solution whose declaration was reconciled by another replica a moment ago is reachable
// here without waiting out the reconcile interval.
//
// The read's failure is CARRIED rather than logged and dropped. It used to be
// dropped, and the empty lookup that followed was then reported as a fact: the
// proxy answered "solution not registered" and admission answered "your
// organization did not install this" because accounts was down. An authority
// that cannot be asked must never read as an answer from it.
func (c *solutionRegistryCache) lookupFresh(
	ctx context.Context, id string, surface routingSurface,
) solutionLookup {
	record, found, loaded := c.lookup(id)
	if c.conclusive(record, found, surface) {
		return solutionLookup{record: record, found: found, loaded: loaded}
	}
	refreshErr := c.refreshIfStale(ctx)
	if refreshErr != nil {
		log.Printf("solution registry: on-demand refresh for %q failed: %v", id, refreshErr)
	}
	record, found, loaded = c.lookup(id)
	return solutionLookup{
		record:      record,
		found:       found,
		loaded:      loaded,
		undecidable: refreshErr != nil && !c.conclusive(record, found, surface),
	}
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
	// TargetID is the immutable solution target this alias resolves to, empty
	// for a registration no declaration opened a target for.
	TargetID string
	// BindingID is the declared binding this alias resolves to. It is carried on
	// the SAME resolution as TargetID and Upstream, for the same reason they are:
	// the audience a presented capability is checked against is derived from it
	// (`solution:<binding-id>`), and a second read could judge a token against the
	// predecessor's audience while traffic went to the replacement's address.
	BindingID string
	// Upstream is the same record's backend address, normalised to scheme+host.
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

// resolveModuleRouting is the same resolution for the /v1/<alias>/* surface. It
// is the same function because the two surfaces must not drift: one lookup, one
// carried resolution, one upstream policy, one tombstone rule, one 120 s
// revocation bound. The only difference is the kind each surface serves.
func (c *solutionRegistryCache) resolveModuleRouting(ctx context.Context, alias string) (*solutionRouting, solutionResolution) {
	return c.resolveRoutingOfKind(ctx, alias, moduleSurface)
}

// routingSurface is which surface is asking. It exists so the kind check is a
// parameter of one resolution rather than a second copy of it.
type routingSurface int

const (
	solutionSurface routingSurface = iota
	moduleSurface
)

func (c *solutionRegistryCache) resolveRoutingOfKind(
	ctx context.Context, id string, surface routingSurface,
) (*solutionRouting, solutionResolution) {
	read := c.lookupFresh(ctx, id, surface)
	record, found := read.record, read.found
	if !read.loaded || read.undecidable {
		return nil, solutionRegistryUnavailable
	}
	// A tombstoned record is in the snapshot so declaration reconciliation and diagnostics
	// can see it, but it routes exactly like an id that never registered. This is
	// also how a module stops being routed: carrier deletion is not revocation,
	// the applied tombstone is, and it reaches this read within the cache bound.
	if !found || record.GetTombstonedAt() != nil {
		return nil, solutionUnregistered
	}
	// The kind the record's DECLARATION states, before anything else about it is
	// read. A record declared as the other kind is a verdict on this surface, not
	// a missing route: the module surface carries no per-viewer admission, so
	// serving a solution here would drop its installation check entirely, and
	// serving a module on the solution surface would ask for a consent no
	// organisation gives a composed module.
	//
	// UNSPECIFIED fails BOTH checks. It is not a third kind — it is what a reader
	// decodes from a writer that did not set the field — so a record carrying it
	// is served by neither surface rather than by the one that needs less
	// authority. accounts and this gateway ship as one module package at one
	// version, which is what makes that refusal a deployment invariant rather
	// than an outage waiting for a rolling update.
	switch surface {
	case moduleSurface:
		// A record with NO declaration fails this too, and must: the module
		// surface has no admission layer behind it, so the kind check is the only
		// place such a record can be refused.
		if !declaredModuleKind(record) {
			return nil, solutionWrongKind
		}
	default:
		// Only a record that IS declared, and declared as the other kind, is
		// refused here. Presence NOTHING declared keeps its existing path: it
		// resolves to no target, and per-viewer admission refuses it with the
		// entitlement refusal header that tells an operator which of the three
		// layers said no. Short-circuiting it here would answer the same 403 while
		// losing that, and "nothing declared this" is a different fact from "a
		// module is declared here".
		if record.GetDeclared() != nil && !declaredSolutionKind(record) {
			return nil, solutionWrongKind
		}
	}
	if !solutionRecordActive(record, surface) {
		return nil, solutionNotActive
	}
	upstream, err := url.Parse(record.GetBackend().GetUpstream())
	if err != nil || upstream.Host == "" ||
		(upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.User != nil ||
		(upstream.Path != "" && upstream.Path != "/") || upstream.RawQuery != "" || upstream.ForceQuery || upstream.Fragment != "" ||
		isDisallowedRegisteredUpstreamHost(upstream.Hostname()) {
		// The deleted write handlers enforced this URL policy. Enforce it at
		// the read boundary before a declared observation becomes a proxy target.
		log.Printf("solution registry: record %q has an unusable upstream", id)
		return nil, solutionNotActive
	}
	return &solutionRouting{
		TargetID:  record.GetDeclared().GetTargetId(),
		BindingID: record.GetDeclared().GetBindingId(),
		Upstream:  &url.URL{Scheme: upstream.Scheme, Host: upstream.Host},
	}, solutionRoutable
}

// solutionRecordActive reads the availability accounts derived from the
// declaration, and checks the halves the SURFACE needs for itself.
//
// Both: the derived status is the authority's answer, and the structural check
// beside it is this gateway refusing to proxy to a half it does not have in hand
// — one read, two reasons, and neither inferred from the other.
//
// The halves differ by kind. A solution serves a page and a backend; a module has
// no browser remote at all, is reached at /v1/<alias>/*, and nothing loads a
// manifest for it — so requiring a frontend half there would leave every module
// permanently unroutable. accounts derives the same distinction in
// SolutionRegistration.Status, which is why the status check below still holds
// for both.
func solutionRecordActive(record *accountsv1.SolutionRegistration, surface routingSurface) bool {
	if record == nil || record.GetTombstonedAt() != nil ||
		record.GetStatus() != accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_ACTIVE {
		return false
	}
	if record.GetBackend() == nil {
		return false
	}
	return surface == moduleSurface || record.GetFrontend() != nil
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
// response. It carries no upstream, manifest, or credential — only the state
// name an operator needs to tell these cases apart.
func solutionRegistryStatusLabel(record *accountsv1.SolutionRegistration) string {
	switch record.GetStatus() {
	case accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_ACTIVE:
		return "active"
	case accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_PENDING:
		return "pending"
	case accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_INCOMPATIBLE:
		return "incompatible"
	case accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_TOMBSTONED:
		return "tombstoned"
	default:
		return fmt.Sprintf("unknown(%d)", record.GetStatus())
	}
}
