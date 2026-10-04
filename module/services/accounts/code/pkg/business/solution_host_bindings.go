package business

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/core/wool"
	"gopkg.in/yaml.v3"
)

// Declared solution presence (issue #952).
//
// Delivery declares which solution runs on this host at which generation. Core
// owns the document and its admission rules; the host reconciles admitted
// declarations into the durable registry.
//
// DESIRED records the newest generation delivery supplied and any refusal.
// APPLIED records the generation reconciled into durable state. Endpoint and
// manifest observations remain separate from that declaration and never grant
// presence or installation authority.

const (
	// SolutionHostBindingReconcileInterval is how often the host re-reads its
	// mount. Delivery replaces a mounted document at its own cadence and gives
	// the host no signal, so the pass is a poll; core's DecisionCurrent exists
	// precisely because the same generation is read over and over.
	SolutionHostBindingReconcileInterval = 30 * time.Second

	// SolutionHostReservedRouteNamespace is the route namespace no delivered
	// binding may claim. Passing it to core reserves "codefly", "codefly/admin"
	// and "codefly.admin" — the host's own surfaces.
	SolutionHostReservedRouteNamespace = "codefly"
)

var (
	// ErrSolutionHostBindingNotDeclared is returned when a binding ID has no
	// durable record.
	ErrSolutionHostBindingNotDeclared = errors.New("solution host binding is not declared")
	// ErrSolutionHostBindingRouteRequired is returned when a present generation
	// does not declare exactly one route alias. core permits several; this host
	// reconciles a binding into one registry record addressed by one path
	// segment, so it needs exactly one and refuses rather than choosing.
	ErrSolutionHostBindingRouteRequired = errors.New("solution host binding must declare exactly one route alias")
	// ErrSolutionHostBindingRouteNotAddressable is returned when the declared
	// alias is not a registry solution id: core's alias vocabulary admits dots
	// and slashes, and a registry key is one lowercase path segment.
	ErrSolutionHostBindingRouteNotAddressable = errors.New("solution host binding route alias is not an addressable solution id")
	// ErrSolutionHostBindingRouteHeld is returned when the registry record the
	// alias resolves to is live and declared by another binding. core's
	// collision check refuses this before a generation applies; this is the
	// durable backstop for two replicas reconciling the same pass.
	ErrSolutionHostBindingRouteHeld = errors.New("solution host binding route alias is held by another binding")
)

// solutionIDPattern is the registry's own identity rule, restated here because
// the host resolves a registry key from a route alias and must refuse an alias
// that could not be one. It is the single-segment gateway route identity.
var solutionIDPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]*[a-z0-9])?$`)

// maxSolutionIDLength matches the registry's own bound on solution_id.
const maxSolutionIDLength = 128

// SolutionHostBindingGeneration is one generation of one document: what it
// declares, and the digest that identifies it. The document is kept whole, not
// just its digest, because the host must be able to say what it is running and
// what it has been asked to run.
type SolutionHostBindingGeneration struct {
	Generation uint64
	Digest     string
	Document   string
	At         time.Time
}

// SolutionHostBindingApplied is the generation this host reconciled, and what it
// reconciled it into.
type SolutionHostBindingApplied struct {
	SolutionHostBindingGeneration
	// Removed records that the applied generation was a tombstone. It is not an
	// absence: the binding ID and its generation remain, so a late or replayed
	// older generation is still refused after a removal.
	Removed bool
	// Routes are the aliases the applied generation holds.
	Routes []string
	// SolutionID is the registry record the applied generation reconciled into.
	// A tombstone generation declares no route, so this is the only way it knows
	// which record to withdraw.
	SolutionID string
	// Release is publisher/name@version of the applied generation.
	Release string
	// Domain is the ownership domain the applied generation declared.
	//
	// It is persisted rather than re-derived because it decides WHO MAY CHANGE
	// this record: core refuses a later generation for the binding that arrives
	// under a different domain, which is what stops one accepted delivery taking
	// over a binding another delivery owns. Re-deriving it from the host's own
	// configured domain list would answer "a domain this host accepts" instead
	// of "the domain this binding was claimed under", and those differ exactly
	// when it matters — a host accepting two domains.
	Domain string
}

// SolutionHostBindingRecord is the host's durable record for one binding ID.
type SolutionHostBindingRecord struct {
	BindingID      string
	HostCoordinate string
	HostComponent  string
	Desired        *SolutionHostBindingGeneration
	Applied        *SolutionHostBindingApplied
	PendingReason  string
	PendingSince   *time.Time
	UpdatedAt      time.Time
}

// AppliedState is the record in the shape core judges a document against. A
// binding with nothing applied contributes no state, which is how core is told
// "never seen".
func (r *SolutionHostBindingRecord) AppliedState() (solutionhost.Applied, bool) {
	if r == nil || r.Applied == nil {
		return solutionhost.Applied{}, false
	}
	return solutionhost.Applied{
		Binding:    r.BindingID,
		Generation: r.Applied.Generation,
		Digest:     r.Applied.Digest,
		Domain:     r.Applied.Domain,
		Routes:     r.Applied.Routes,
		Removed:    r.Applied.Removed,
	}, true
}

// PendingGeneration is the generation delivery is showing that this host has not
// applied, or zero when desired and applied agree. It is what an operator reads
// beside PendingReason.
func (r *SolutionHostBindingRecord) PendingGeneration() uint64 {
	if r == nil || r.Desired == nil {
		return 0
	}
	if r.Applied != nil && r.Applied.Generation == r.Desired.Generation && r.Applied.Digest == r.Desired.Digest {
		return 0
	}
	return r.Desired.Generation
}

// SolutionHostBindingDocument is one delivered document as it was read. Source
// is where it came from and appears only in operator-facing messages; identity
// comes from the document's own binding field, never from a file name.
type SolutionHostBindingDocument struct {
	Source string

	// Data is a SIGNED CARRIER — core's {schema, document, bundle} — not a bare
	// presence document. Delivery signs at publish and the host verifies at
	// admission, so the bytes that cross the mount are the bytes the signature
	// covers. A bare document now fails to parse here rather than being
	// admitted unverified, which is the point: core cd443989 removed every path
	// from unattested bytes to a host judgement, and this is the host side of
	// that removal.
	Data []byte
}

// SolutionHostBindingSource is the delivered desired set.
//
// It returns an error rather than an empty set when the mount cannot be read.
// Removal is a tombstone generation precisely so that a lost, unmounted or
// unreadable mount can never be reconciled as "remove everything".
type SolutionHostBindingSource interface {
	Documents(ctx context.Context) ([]SolutionHostBindingDocument, error)
}

// SolutionHostBindingReconciler drives one pass and, when started, repeats it.
type SolutionHostBindingReconciler struct {
	service *Service
	source  SolutionHostBindingSource
	host    solutionhost.Host

	// verifier is this host's attestation check. core holds no verifier and
	// never will: signing is keyless over a workload identity, so verifying is
	// a trust root and an identity policy that belong to the deployment, not to
	// a library every binary imports.
	verifier solutionhost.BundleVerifier

	interval time.Duration
	now      func() time.Time

	// activation answers whether delivered authority is active for a binding at
	// a build. It lives on the reconciler because the reconciler already holds
	// every policy input activation needs — this host's coordinate, the
	// ownership domains it accepts, which signer may speak for which, and the
	// bundle verifier — and a second copy of any of them would be a second
	// answer to "what does this host accept".
	activation *SolutionAuthorityActivation

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// SolutionHostBindingReconcilerConfig is everything one host's reconciler needs
// to judge delivery. Every field but Interval is required, and each absence is
// refused by name rather than defaulted, because every one of them is a
// different way for the host to accept a binding it should not.
type SolutionHostBindingReconcilerConfig struct {
	// Source is the delivered desired set, as signed carriers.
	Source SolutionHostBindingSource

	// Verifier is this host's attestation check over a carrier's bundle. It is
	// required: without one there is no way to construct the verified value
	// core's Admit takes, so a host with no verifier admits nothing at all
	// rather than falling back to admitting everything.
	Verifier solutionhost.BundleVerifier

	// Coordinate is the host this reconciler answers for.
	Coordinate string

	// Domains are the ownership domains this host accepts delivery under.
	Domains []string

	// DomainsBySigner is which of those domains each attested signer identity
	// may speak for, keyed by the identity Verifier returns. A document asserts
	// its own ownership domain, so without this policy any signer the host
	// accepted at all could deliver under any domain the host accepted and take
	// over bindings in it.
	DomainsBySigner map[string][]string

	// Interval between passes. Zero takes the default.
	Interval time.Duration

	// Envelope is the AUTHORITY CEILING delivered to this host: who may hold
	// which binding, and which builds are approved, at one revision.
	//
	// Optional, and its absence is not a weaker ceiling. A zero revision means
	// no ceiling is delivered, and every activation then refuses with
	// ErrSolutionAuthorityCeilingUnavailable — the host has decided nothing
	// rather than decided that a grant is absent. A host that reconciles
	// presence with no ceiling delivered is a correct, complete deployment:
	// authority is simply not a question it can answer, and making the presence
	// half depend on the authority half being configured would be the wrong
	// coupling.
	//
	// It is never read out of a delivered document. core: "an envelope a
	// document carried would be a document declaring its own ceiling", and
	// ValidateAgainst tests a document's grants and approved build against the
	// envelope's — so an envelope assembled from the delivery tree answers
	// itself for every document in it.
	Envelope solutionhost.Envelope
}

// NewSolutionHostBindingReconciler builds the reconciler for one host
// coordinate. The coordinate is required: core refuses a document that targets
// another host, and a host that did not know its own coordinate would reconcile
// whatever it was handed.
func NewSolutionHostBindingReconciler(
	service *Service, config SolutionHostBindingReconcilerConfig,
) (*SolutionHostBindingReconciler, error) {
	source, domains, interval := config.Source, config.Domains, config.Interval
	if service == nil || source == nil {
		return nil, errors.New("solution host binding reconciler requires a service and a source")
	}
	if config.Verifier == nil {
		return nil, errors.New("solution host binding reconciler requires a bundle verifier; a host that cannot verify a carrier must admit nothing, not everything")
	}
	if config.Coordinate == "" {
		return nil, errors.New("solution host binding reconciler requires this host's coordinate")
	}
	// Core requires the accepted ownership domains whenever a coordinate is set,
	// and refuses a document from an unstated one. The reason is that the applied
	// record cannot bound a binding's FIRST generation — there is nothing to
	// compare against yet — so without this, any delivery could claim any unseen
	// binding ID under a domain of its choosing and own it from then on. It is the
	// host's to declare because core cannot know which delivery is entitled to a
	// name it has never seen.
	if len(domains) == 0 {
		return nil, errors.New("solution host binding reconciler requires the ownership domains this host accepts delivery from")
	}
	// Same reasoning one axis over, and core refuses the call without it: an
	// unstated signer policy would let every accepted signer claim every
	// accepted domain, which makes the domain list decorative.
	if len(config.DomainsBySigner) == 0 {
		return nil, errors.New("solution host binding reconciler requires the ownership domains each signer identity may deliver under")
	}
	if interval <= 0 {
		interval = SolutionHostBindingReconcileInterval
	}
	reconciler := &SolutionHostBindingReconciler{
		service:  service,
		source:   source,
		verifier: config.Verifier,
		host: solutionhost.Host{
			Coordinate:      config.Coordinate,
			Domains:         domains,
			DomainsBySigner: config.DomainsBySigner,
			Reserved:        []string{SolutionHostReservedRouteNamespace},
		},
		interval: interval,
		now:      func() time.Time { return time.Now().UTC() },
	}
	activation, err := newSolutionAuthorityActivation(reconciler, config.Envelope)
	if err != nil {
		return nil, err
	}
	reconciler.activation = activation
	return reconciler, nil
}

// AuthorityActivation is the seam a capability-minting path asks "is the
// delivered authority over this binding active for the build I have established
// this caller is running".
//
// It is exposed rather than consumed here because the host does not APPLY
// authority to anything: there is no durable state an activation reconciles
// into, and a reconcile pass that logged the answer every interval would be
// noise rather than an effect. The answer is a read, and the caller that needs
// it is the one that has independently established a running build.
func (r *SolutionHostBindingReconciler) AuthorityActivation() *SolutionAuthorityActivation {
	if r == nil {
		return nil
	}
	return r.activation
}

// RunOnce reads the mount and reconciles one pass.
//
// The pass is ordered so that a refusal can never disturb what is running.
// Everything is read and judged before anything is written: the mount, then the
// durable applied state, then core's verdict on the whole desired set. Only then
// is desired state recorded — for every delivered binding, admitted or not, so a
// withheld generation and its reason are visible — and only then is an admitted
// generation applied.
func (r *SolutionHostBindingReconciler) RunOnce(ctx context.Context) error {
	documents, err := r.source.Documents(ctx)
	if err != nil {
		// An unreadable mount is not an empty desired set. Nothing is recorded
		// and nothing is applied: the last generation that passed keeps running.
		return fmt.Errorf("read delivered solution host bindings: %w", err)
	}

	verified, unparsed := verifySolutionHostBindingDocuments(ctx, r.verifier, documents)

	records, err := r.service.ListSolutionHostBindings(ctx)
	if err != nil {
		return err
	}
	host := r.host
	host.Applied = appliedStates(records)

	admission := admitSolutionHostBindings(host, verified)

	// Desired state is recorded for every delivered document before anything is
	// applied, so an operator sees what delivery is showing even when none of it
	// passed. A document that did not parse names no binding it can be recorded
	// against; those are reported to the caller.
	// A generation core reports as already applied is settled: recording desired
	// state for it may clear a reason left by an earlier pass, because there is
	// nothing outstanding. A generation about to be applied is not settled — the
	// apply owns its reason.
	settled := make(map[string]bool, len(admission.Accepted))
	for _, accepted := range admission.Accepted {
		settled[accepted.Document.Binding] = accepted.Decision == solutionhost.DecisionCurrent
	}
	var failures []error
	for _, one := range verified {
		document := one.Document
		reason := admission.Withheld[document.Binding]
		if err := r.service.recordSolutionHostBindingDesired(
			ctx, document, reason, settled[document.Binding], r.now(),
		); err != nil {
			failures = append(failures, fmt.Errorf("record desired generation for binding %q: %w", document.Binding, err))
		}
	}

	for _, applied := range orderSolutionHostBindingApplies(admission.Accepted, records) {
		if applied.Decision != solutionhost.DecisionApply {
			// DecisionCurrent: this exact generation is already applied. A host
			// re-reads its mount every pass, so this is the normal answer and
			// not an error. Desired state was recorded above; there is nothing
			// to change.
			continue
		}
		if err := r.service.applySolutionHostBinding(
			ctx, applied.Delivered, r.host.Coordinate, r.host.Domains, r.host.DomainsBySigner, r.now(),
		); err != nil {
			// One binding's apply failing leaves every other binding's progress
			// intact: each apply is its own transaction, because each binding is
			// independent desired state. The reason is recorded as this
			// binding's pending state so it is visible rather than log-only.
			if recordErr := r.service.recordSolutionHostBindingRefusal(
				ctx, applied.Document.Binding, err.Error(), r.now(),
			); recordErr != nil {
				failures = append(failures, recordErr)
			}
			failures = append(failures, fmt.Errorf("apply binding %q generation %d: %w",
				applied.Document.Binding, applied.Document.Generation, err))
		}
	}

	// A document core refused to parse still gets its refusal recorded against
	// the binding it names, so "delivery is shipping something this host cannot
	// read" is visible beside the generation it last applied rather than only in
	// a log line.
	for _, problem := range unparsed {
		failures = append(failures, problem.err)
		if problem.binding == "" {
			continue
		}
		if err := r.service.recordSolutionHostBindingRefusal(
			ctx, problem.binding, problem.err.Error(), r.now(),
		); err != nil && !errors.Is(err, ErrSolutionHostBindingNotDeclared) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Start runs a pass now and then on every interval until Shutdown. A failing
// pass is logged and the loop continues: the mount is re-read every interval, so
// a transient failure resolves itself and a persistent one keeps saying so.
func (r *SolutionHostBindingReconciler) Start(parent context.Context) {
	r.mu.Lock()
	if r.cancel != nil {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	r.done = make(chan struct{})
	done := r.done
	r.mu.Unlock()

	go func() {
		defer close(done)
		run := func() {
			if err := r.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				wool.Get(ctx).In("business.solution_host_bindings").Warn(
					"solution host binding reconcile pass failed", wool.ErrField(err))
			}
		}
		run()
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}

// Shutdown stops the loop and waits for the pass in flight.
func (r *SolutionHostBindingReconciler) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	r.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// verifySolutionHostBindingDocuments verifies every delivered carrier, keeping
// the ones this host's attestation check accepts and describing the ones it does
// not.
//
// Every refusal here is one of three things, and they are deliberately not
// distinguished in what is recorded: not a carrier, a carrier whose bundle does
// not verify, or a verified payload core will not parse. An operator needs to
// know delivery is shipping something this host will not admit; which of the
// three it is tells an attacker which half of the door it got past, so the
// reason recorded against a binding stays coarse while the returned error — which
// goes to the log, not the row — carries core's own wording.
//
// Attribution of a refusal to a binding ID reads UNVERIFIED bytes, and that is
// a deliberate, bounded choice. A carrier that fails verification has no
// attested signer by definition, so there is no verified name to attribute it
// to; recording nothing would hide "delivery is broken for this binding" in a
// log line. What it can do is write a refusal REASON onto an existing binding's
// row — it cannot change what that binding is running, cannot create a row
// (recordSolutionHostBindingRefusal answers ErrSolutionHostBindingNotDeclared
// for an undeclared binding), and cannot reach Admit. So the residual is a
// misleading reason string on a row whose applied generation is untouched, which
// is why the attribution is kept and why the reason is coarse.
func verifySolutionHostBindingDocuments(
	ctx context.Context, verifier solutionhost.BundleVerifier, documents []SolutionHostBindingDocument,
) ([]deliveredSolutionHostBinding, []unparsedSolutionHostBinding) {
	verified := make([]deliveredSolutionHostBinding, 0, len(documents))
	var problems []unparsedSolutionHostBinding
	for _, delivered := range documents {
		carrier, err := solutionhost.ParseSigned(delivered.Data)
		if err != nil {
			problems = append(problems, unparsedSolutionHostBinding{
				err: fmt.Errorf("delivered solution host binding %s: %w", delivered.Source, err),
			})
			continue
		}
		one, err := solutionhost.VerifyDelivered(ctx, carrier, verifier)
		if err != nil {
			problems = append(problems, unparsedSolutionHostBinding{
				binding: attributeSolutionHostBinding(carrier.Document),
				err:     fmt.Errorf("delivered solution host binding %s: %w", delivered.Source, err),
			})
			continue
		}
		document, err := one.Document()
		if err != nil {
			// core parsed these same bytes inside VerifyDelivered, so this is
			// unreachable by construction rather than merely unlikely. It is
			// still handled as a refusal: a host that cannot read back what it
			// just verified must admit nothing from it, and the alternative is
			// a nil document reaching admission.
			problems = append(problems, unparsedSolutionHostBinding{
				binding: attributeSolutionHostBinding(carrier.Document),
				err:     fmt.Errorf("delivered solution host binding %s: %w", delivered.Source, err),
			})
			continue
		}
		verified = append(verified, deliveredSolutionHostBinding{Delivered: one, Document: document})
	}
	sort.Slice(verified, func(i, j int) bool { return verified[i].Document.Binding < verified[j].Document.Binding })
	return verified, problems
}

// deliveredSolutionHostBinding pairs an attested carrier with the document
// re-derived from its attested bytes, derived ONCE per pass.
//
// core 67ee7220 made Delivered.Document() a derivation returning an error
// rather than a field returning a pointer, because the pointer form let a
// caller mutate what Admit would afterwards consume — the attestation covered
// one generation and admission consumed another, with nothing anywhere saying
// so. Two consequences land here. A derivation that can fail cannot be read
// inside a sort comparator, and re-reading it per use would re-parse and
// canonically round-trip the same bytes once for every question asked of them.
//
// Holding the result is safe in a way holding the old pointer was not: Admit
// re-derives from the attested payload itself, so this copy is what the pass
// READS and cannot become what the host admits. That is the property core's
// redesign bought, and pairing the two values here is what makes the pass use
// one document rather than a fresh one per call site.
type deliveredSolutionHostBinding struct {
	Delivered *solutionhost.Delivered
	Document  *solutionhost.SolutionHostBinding
}

// unparsedSolutionHostBinding is a delivered document core would not parse, and
// the binding it claims to be for.
type unparsedSolutionHostBinding struct {
	binding string
	err     error
}

// attributeSolutionHostBinding reads only the binding ID out of a document this
// host refused, so the refusal can be recorded against it. It never produces a
// document and its result is never admitted. Its input is unverified by
// construction — see verifySolutionHostBindingDocuments for why that is bounded.
func attributeSolutionHostBinding(data []byte) string {
	var named struct {
		Binding string `yaml:"binding"`
	}
	if err := yaml.Unmarshal(data, &named); err != nil {
		return ""
	}
	return named.Binding
}

func appliedStates(records []*SolutionHostBindingRecord) []solutionhost.Applied {
	applied := make([]solutionhost.Applied, 0, len(records))
	for _, record := range records {
		if state, ok := record.AppliedState(); ok {
			applied = append(applied, state)
		}
	}
	return applied
}

// orderSolutionHostBindingApplies puts the generations that RELEASE a registry
// key before the ones that claim one.
//
// A route alias moving from one binding to another is two applies, and each
// binding is its own transaction. Applying the claimant first would collide with
// the key the previous holder still records — the partial unique index refuses
// it — and the hand-over would only complete on a later pass. Releasing first
// completes it in one.
func orderSolutionHostBindingApplies(
	accepted []SolutionHostBindingDecision, records []*SolutionHostBindingRecord,
) []SolutionHostBindingDecision {
	held := make(map[string]string, len(records))
	for _, record := range records {
		if record.Applied != nil && !record.Applied.Removed && record.Applied.SolutionID != "" {
			held[record.BindingID] = record.Applied.SolutionID
		}
	}
	releases := func(candidate SolutionHostBindingDecision) bool {
		current, holds := held[candidate.Document.Binding]
		if !holds {
			return false
		}
		if candidate.Document.Removed {
			return true
		}
		for _, alias := range candidate.Document.Aliases() {
			if alias == current {
				return false
			}
		}
		return true
	}
	ordered := make([]SolutionHostBindingDecision, 0, len(accepted))
	for _, candidate := range accepted {
		if releases(candidate) {
			ordered = append(ordered, candidate)
		}
	}
	for _, candidate := range accepted {
		if !releases(candidate) {
			ordered = append(ordered, candidate)
		}
	}
	return ordered
}

// solutionHostBindingRegistryKey resolves the registry record a present
// generation reconciles into.
//
// The key is the binding's route alias, because the alias is what this host
// routes on: it is the <id> in /solutions/<id>/* and the key the frontend loads
// a remote under. The binding ID is deliberately not the key — it identifies a
// deployment instance, may carry characters a path segment may not, and a second
// instance of the same solution inherits nothing from the first, alias included.
func solutionHostBindingRegistryKey(document *solutionhost.SolutionHostBinding) (string, error) {
	aliases := document.Aliases()
	if len(aliases) != 1 {
		return "", fmt.Errorf("%w: binding %q declares %d",
			ErrSolutionHostBindingRouteRequired, document.Binding, len(aliases))
	}
	alias := aliases[0]
	if !solutionIDPattern.MatchString(alias) || len(alias) > maxSolutionIDLength {
		return "", fmt.Errorf("%w: binding %q declares route %q, which is not one lowercase path segment",
			ErrSolutionHostBindingRouteNotAddressable, document.Binding, alias)
	}
	return alias, nil
}
