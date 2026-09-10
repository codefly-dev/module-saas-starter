package main

// The gateway's client and cache for the durable solution registry (#534).
//
// Registrations used to live in a process-local map here and a second one in
// the frontend. A restart lost them, a registration behind a load balancer
// reached one replica, and the two halves could disagree. The authority is now
// accounts (solution_registrations); this file is the gateway's only view of
// it: a snapshot rebuilt from ListSolutionRegistrations on a timer, on demand
// after a local write, and once on a cache miss.
//
// The gateway is the registry's only client. It owns its own backend half and
// brokers the frontend's half and the frontend's reads, for the same reason it
// brokers module-registration minting: the accounts internal listener admits
// this service account and nothing else.

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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	putSolutionRegistrationMethod    = "/saas.accounts.v1.SolutionRegistryService/PutSolutionRegistration"
	deleteSolutionRegistrationMethod = "/saas.accounts.v1.SolutionRegistryService/DeleteSolutionRegistration"
	listSolutionRegistrationsMethod  = "/saas.accounts.v1.SolutionRegistryService/ListSolutionRegistrations"
)

const (
	// solutionRegistryCallTimeout bounds one registry round trip. A registrant
	// blocks on this during its startup, so a stalled accounts must surface as
	// a failed registration rather than a hung boot.
	solutionRegistryCallTimeout = 10 * time.Second
	// solutionReconcileInterval is the convergence bound: after a write on any
	// replica, every other replica reflects it within one interval plus the
	// round trip. It is also the cadence at which a replica notices a
	// registration whose lease lapsed.
	solutionReconcileInterval = 10 * time.Second
	// solutionRefreshFloor rate-limits the on-demand refresh a cache miss
	// triggers, so a flood of requests for an unregistered id cannot turn into
	// a flood of registry reads. It is short because it is also the worst-case
	// convergence delay for a request that arrives at a replica the
	// registration has not reached yet.
	solutionRefreshFloor = 250 * time.Millisecond
	// solutionLease is the liveness window a registrant is granted. It is
	// comfortably longer than the reconcile interval, so a registrant that
	// renews at any sane cadence never flickers out of the snapshot.
	solutionLease = 120 * time.Second
)

// solutionRegistryClient is the registry surface the gateway consumes. The
// interface exists so the cache and the HTTP handlers can be exercised without
// an accounts process.
type solutionRegistryClient interface {
	Put(ctx context.Context, req *accountsv1.PutSolutionRegistrationRequest) (*accountsv1.SolutionRegistration, error)
	Delete(ctx context.Context, req *accountsv1.DeleteSolutionRegistrationRequest) (*accountsv1.SolutionRegistration, error)
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

func (c *accountsSolutionRegistry) Put(ctx context.Context, req *accountsv1.PutSolutionRegistrationRequest) (*accountsv1.SolutionRegistration, error) {
	resp := &accountsv1.SolutionRegistration{}
	return resp, c.invoke(ctx, putSolutionRegistrationMethod, req, resp)
}

func (c *accountsSolutionRegistry) Delete(ctx context.Context, req *accountsv1.DeleteSolutionRegistrationRequest) (*accountsv1.SolutionRegistration, error) {
	resp := &accountsv1.SolutionRegistration{}
	return resp, c.invoke(ctx, deleteSolutionRegistrationMethod, req, resp)
}

func (c *accountsSolutionRegistry) List(ctx context.Context, req *accountsv1.ListSolutionRegistrationsRequest) (*accountsv1.ListSolutionRegistrationsResponse, error) {
	resp := &accountsv1.ListSolutionRegistrationsResponse{}
	return resp, c.invoke(ctx, listSolutionRegistrationsMethod, req, resp)
}

// solutionResolution is why a solution is or is not routable right now. The
// distinctions are the ones an operator needs: never registered, registered but
// not yet whole, registered and gone stale, and "we cannot tell".
type solutionResolution int

const (
	solutionRoutable solutionResolution = iota
	solutionUnregistered
	solutionNotActive
	solutionRegistryUnavailable
)

// errSolutionRegistryUnconfigured is what a registry call returns when the
// gateway has no accounts connection. It cannot happen in a deployed gateway —
// main always wires one — and exists so a misconfigured process fails loudly
// instead of silently serving an empty registry.
var errSolutionRegistryUnconfigured = errors.New("solution registry client not configured")

// solutionRegistryCache is the gateway's replica-local view of the registry.
type solutionRegistryCache struct {
	client solutionRegistryClient
	// now is the clock the lease checks read. Tests move it; production leaves
	// it at time.Now.
	now func() time.Time

	mu         sync.RWMutex
	records    map[string]*accountsv1.SolutionRegistration
	revision   int64
	loaded     bool
	loadedAt   time.Time
	lastErr    error
	refreshing sync.Mutex
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
// but a re-registration has to name the tombstone's revision to be admitted,
// and an operator reading the registry has to be able to see it.
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
func (c *solutionRegistryCache) refreshIfStale(ctx context.Context) error {
	c.refreshing.Lock()
	defer c.refreshing.Unlock()
	if !c.staleEnoughToRefresh() {
		return nil
	}
	return c.load(ctx)
}

// load performs the read itself. The caller holds c.refreshing.
func (c *solutionRegistryCache) load(ctx context.Context) error {
	if c.client == nil {
		return errSolutionRegistryUnconfigured
	}
	resp, err := c.client.List(ctx, &accountsv1.ListSolutionRegistrationsRequest{IncludeTombstoned: true})
	c.mu.Lock()
	defer c.mu.Unlock()
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
	c.loadedAt = c.now()
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
	return !c.loaded || c.now().Sub(c.loadedAt) >= solutionRefreshFloor
}

// resolve answers where to proxy a /solutions/<id>/* request, and why not when
// it cannot. A miss triggers at most one on-demand refresh (floor-limited), so
// a solution that registered against another replica a moment ago is reachable
// here without waiting out the reconcile interval.
func (c *solutionRegistryCache) resolve(ctx context.Context, id string) (*url.URL, solutionResolution) {
	record, found, loaded := c.lookup(id)
	if !found || !solutionRecordActive(record, c.now()) {
		if err := c.refreshIfStale(ctx); err != nil {
			log.Printf("solution registry: on-demand refresh for %q failed: %v", id, err)
		}
		record, found, loaded = c.lookup(id)
	}
	if !loaded {
		return nil, solutionRegistryUnavailable
	}
	// A tombstoned record is in the snapshot so re-registration and diagnostics
	// can see it, but it routes exactly like an id that never registered.
	if !found || record.GetTombstonedAt() != nil {
		return nil, solutionUnregistered
	}
	if !solutionRecordActive(record, c.now()) {
		return nil, solutionNotActive
	}
	upstream, err := url.Parse(record.GetBackend().GetUpstream())
	if err != nil || upstream.Host == "" {
		// The upstream was validated before it was stored, so this is a
		// corrupted record rather than a caller error.
		log.Printf("solution registry: record %q has an unusable upstream", id)
		return nil, solutionNotActive
	}
	return &url.URL{Scheme: upstream.Scheme, Host: upstream.Host}, solutionRoutable
}

// solutionRecordActive re-derives activity here rather than trusting the status
// accounts stamped: the status was computed when the snapshot was read, and a
// lease can lapse while the snapshot is still cached.
func solutionRecordActive(record *accountsv1.SolutionRegistration, now time.Time) bool {
	if record == nil ||
		record.GetStatus() != accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_ACTIVE {
		return false
	}
	return record.GetFrontend().GetLeaseExpiresAt().AsTime().After(now) &&
		record.GetBackend().GetLeaseExpiresAt().AsTime().After(now)
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

// expectedRevisionFor picks the compare-and-swap token for a write, and is the
// single place that rule lives: the handler and the conflict retry must agree,
// or the retry re-derives a token the handler deliberately withheld.
//
// Withholding the token on a tombstone is what refuses a retiring deployment's
// heartbeat — the registry rejects a tombstoned write that names no revision.
// Only a caller that explicitly asked to re-register may name it.
func (c *solutionRegistryCache) expectedRevisionFor(id string, reactivate bool) *int64 {
	record, ok, _ := c.lookup(id)
	if !ok {
		return nil
	}
	if record.GetTombstonedAt() != nil && !reactivate {
		return nil
	}
	revision := record.GetRevision()
	return &revision
}

// write performs one registration write, refreshing this replica immediately on
// success so the registrant's very next request — which may land here — already
// sees it.
//
// A write that changes an existing half needs the record's current revision.
// This replica's cached revision may be behind, so an Aborted refusal is
// retried once against a freshly read snapshot. A second refusal is returned to
// the caller: two publishers are racing, and picking a winner by looping is
// worse than telling one of them to try again.
// retoken re-derives the compare-and-swap token after a refresh. It is supplied
// by the caller rather than reconstructed here so the initial token and the
// retry token are, structurally, the same rule: a second copy of that rule is
// exactly how a retry once handed a plain heartbeat a tombstone's revision.
func (c *solutionRegistryCache) write(
	ctx context.Context, req *accountsv1.PutSolutionRegistrationRequest, retoken func() *int64,
) (*accountsv1.SolutionRegistration, error) {
	if c.client == nil {
		return nil, errSolutionRegistryUnconfigured
	}
	record, err := c.client.Put(ctx, req)
	// Aborted is the registry saying "your view is behind". That covers a token
	// that lost a race and, just as importantly, no token at all because this
	// replica had not seen the record yet — the case a refresh actually fixes.
	// A tombstone refusal is FailedPrecondition and deliberately does not land
	// here, so retrying cannot resurrect a removed registration.
	if status.Code(err) == codes.Aborted {
		if refreshErr := c.refresh(ctx); refreshErr != nil {
			return nil, err
		}
		req.ExpectedRevision = retoken()
		record, err = c.client.Put(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	if refreshErr := c.refresh(ctx); refreshErr != nil {
		log.Printf("solution registry: post-write refresh failed for %q: %v", req.GetSolutionId(), refreshErr)
	}
	return record, nil
}

// remove tombstones a registration and refreshes this replica, so the route
// disappears here before the response is written rather than at the next tick.
func (c *solutionRegistryCache) remove(ctx context.Context, id string) (*accountsv1.SolutionRegistration, error) {
	if c.client == nil {
		return nil, errSolutionRegistryUnconfigured
	}
	record, err := c.client.Delete(ctx, &accountsv1.DeleteSolutionRegistrationRequest{SolutionId: id})
	if err != nil {
		return nil, err
	}
	if refreshErr := c.refresh(ctx); refreshErr != nil {
		log.Printf("solution registry: post-delete refresh failed for %q: %v", id, refreshErr)
	}
	return record, nil
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
func solutionRegistryStatusLabel(record *accountsv1.SolutionRegistration, now time.Time) string {
	switch record.GetStatus() {
	case accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_ACTIVE:
		if !solutionRecordActive(record, now) {
			// The lease lapsed between the snapshot and this read.
			return "expired"
		}
		return "active"
	case accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_PENDING:
		return "pending"
	case accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_EXPIRED:
		return "expired"
	case accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_INCOMPATIBLE:
		return "incompatible"
	case accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_TOMBSTONED:
		return "tombstoned"
	default:
		return fmt.Sprintf("unknown(%d)", record.GetStatus())
	}
}
