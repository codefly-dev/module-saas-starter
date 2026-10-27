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

// Declared module and solution presence (issue #952).
//
// Delivery declares, in one signed module manifest per binding, which module
// or solution runs on this host at which generation, which workloads run as
// it, where its endpoints are and what authority it asks for. Core owns the
// document and its admission rules; the host reconciles admitted manifests into
// the durable registry and derives everything else from the applied one.
//
// DESIRED records the newest generation delivery supplied and any refusal.
// APPLIED records the generation reconciled into durable state.

const (
	// SolutionHostBindingReconcileInterval is how often the host re-reads its
	// inbox when nothing asked for a pass sooner. A delivery and an envelope
	// write trigger one, so the tick is the convergence bound for everything
	// else (a re-pinned key, a retried apply).
	SolutionHostBindingReconcileInterval = 30 * time.Second

	// SolutionHostReservedRouteNamespace is the route namespace no delivered
	// binding may claim. Passing it to core reserves "codefly", "codefly/admin"
	// and "codefly.admin" — the host's own surfaces.
	SolutionHostReservedRouteNamespace = "codefly"
)

var (
	ErrSolutionFrontendNotPrepared = errors.New("signed frontend payload is not prepared for immutable serving")
	// ErrSolutionHostBindingNotDeclared is returned when a binding ID has no
	// durable record.
	ErrSolutionHostBindingNotDeclared = errors.New("solution host binding is not declared")
	// ErrSolutionHostBindingRouteNotAddressable is returned when the declared
	// alias is not a registry solution id: core's alias is one lowercase
	// segment that may hold dots, and a registry key may not.
	ErrSolutionHostBindingRouteNotAddressable = errors.New("solution host binding route alias is not an addressable solution id")
	// ErrSolutionHostBindingKindNotRoutable is returned when a present generation
	// declares a kind this host has no routing surface for. core admits only
	// "solution" and "module", so this is reachable only from a core whose
	// vocabulary grew past this host's — and the answer is a refusal that leaves
	// the running generation alone, not a record no surface serves.
	ErrSolutionHostBindingKindNotRoutable = errors.New("solution host binding declares a kind this host does not route")
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

// SolutionHostBindingGeneration is one generation of one manifest: what it
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
	// Removed records that the applied generation was a withdrawal. It is not
	// an absence: the binding ID and its generation remain, so a late or
	// replayed generation is still refused after a removal.
	Removed bool
	// Routes are the aliases the applied generation holds.
	Routes []string
	// SolutionID is the registry record the applied generation reconciled into.
	// A withdrawal is routed nowhere, so this is how it knows which record to
	// withdraw.
	SolutionID string
	// Release is publisher/name@version of the applied generation.
	Release string
	// Domain is the ownership domain the applied generation declared.
	//
	// It is persisted as well as held in the applied manifest because it
	// decides WHO MAY CHANGE this record: core refuses a later generation for
	// the binding that arrives under a different domain.
	Domain string
}

// SolutionHostBindingRecord is the host's durable record for one binding ID.
type SolutionHostBindingRecord struct {
	BindingID      string
	HostCoordinate string
	// Domain is the ownership domain the binding first applied under. It is
	// kept when live applied state is not — migration 41 cleared that and kept
	// this — so a binding awaiting redelivery still belongs to its domain, and
	// no other pinned domain can take its id (and its installations).
	Domain        string
	Desired       *SolutionHostBindingGeneration
	Applied       *SolutionHostBindingApplied
	PendingReason string
	PendingSince  *time.Time
	UpdatedAt     time.Time
}

// ownershipDomain is the domain the binding belongs to, or "" for a binding
// no generation was ever applied for.
func (r *SolutionHostBindingRecord) ownershipDomain() string {
	switch {
	case r == nil:
		return ""
	case r.Domain != "":
		return r.Domain
	case r.Applied != nil:
		return r.Applied.Domain
	}
	return ""
}

// checkOwnershipDomain refuses a manifest for this binding under another
// ownership domain than the one it belongs to — core's Admit does the same
// against an applied manifest, and this holds when none remains.
func (r *SolutionHostBindingRecord) checkOwnershipDomain(candidate *solutionhost.ModuleManifest) error {
	if domain := r.ownershipDomain(); domain != "" && candidate.Host.Domain != domain {
		return fmt.Errorf("%w: binding %q belongs to ownership domain %q, and this manifest speaks for %q",
			ErrSolutionDeliveryDomainNotGranted, candidate.Binding, domain, candidate.Host.Domain)
	}
	return nil
}

// AppliedManifest is the manifest this host applied for the binding, the one
// core's Admit judges a candidate against; nil when nothing was applied.
//
// A withdrawal recorded before the module manifest replaced the presence
// document cannot be read as a manifest, and it is still terminal: it answers
// core's ErrWithdrawn, never "nothing applied". Any other unreadable record is
// the host's own damaged state (ErrAppliedUnusable).
func (r *SolutionHostBindingRecord) AppliedManifest() (*solutionhost.ModuleManifest, error) {
	if r == nil || r.Applied == nil {
		return nil, nil
	}
	manifest, err := solutionhost.Parse([]byte(r.Applied.Document))
	if err != nil {
		if r.Applied.Removed {
			return nil, fmt.Errorf("%w: binding %q was withdrawn at generation %d, before this host read module manifests",
				solutionhost.ErrWithdrawn, r.BindingID, r.Applied.Generation)
		}
		return nil, fmt.Errorf("%w: binding %q applied generation %d: %v",
			solutionhost.ErrAppliedUnusable, r.BindingID, r.Applied.Generation, err)
	}
	if manifest.Binding != r.BindingID || manifest.Generation != r.Applied.Generation || manifest.Removed != r.Applied.Removed {
		return nil, fmt.Errorf("%w: binding %q applied generation %d records a manifest for %q generation %d",
			solutionhost.ErrAppliedUnusable, r.BindingID, r.Applied.Generation, manifest.Binding, manifest.Generation)
	}
	return manifest, nil
}

// appliedManifests is every readable applied manifest: the incumbents a
// candidate's route alias is held against. An unreadable record claims no
// alias here; its own binding's admission reports it.
func appliedManifests(records []*SolutionHostBindingRecord) []*solutionhost.ModuleManifest {
	applied := make([]*solutionhost.ModuleManifest, 0, len(records))
	for _, record := range records {
		if manifest, err := record.AppliedManifest(); err == nil && manifest != nil {
			applied = append(applied, manifest)
		}
	}
	return applied
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

// SolutionHostBindingDocument is one delivered manifest carrier as it was read.
// Source is where it came from and appears only in operator-facing messages;
// identity comes from the manifest's own binding field.
type SolutionHostBindingDocument struct {
	Source string

	// Data is a SIGNED CARRIER — core's {document, envelope} — never a bare
	// manifest, so the bytes the pass judges are the bytes the signature
	// covers.
	Data []byte
}

// SolutionHostBindingSource is the delivered desired set.
//
// It returns an error rather than an empty set when the inbox cannot be read.
// Removal is a withdrawal generation precisely so that a lost or unreadable
// input can never be reconciled as "remove everything".
type SolutionHostBindingSource interface {
	Documents(ctx context.Context) ([]SolutionHostBindingDocument, error)
}

// SolutionHostBindingReconciler drives one pass and, when started, repeats it.
type SolutionHostBindingReconciler struct {
	service    *Service
	source     SolutionHostBindingSource
	coordinate string

	// verifier uses the platform's pins; core checks the envelope and the
	// signer's ownership domain before a manifest reaches admission.
	verifier *solutionhost.Verifier

	interval time.Duration
	now      func() time.Time

	// modulePrincipals derives the module registry every capability decision
	// reads, from the applied manifests and the current envelope, and swaps it
	// into the service each pass.
	modulePrincipals *ModulePrincipalReconciler

	// afterPass runs at the end of every pass, once what the pass delivered is
	// installable (AfterEachPass).
	afterPass func(context.Context) error

	// trigger asks the loop for a pass now. Buffered by one: a request made
	// while a pass is pending is already satisfied by that pass.
	trigger chan struct{}

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// SolutionHostBindingReconcilerConfig is everything one host's reconciler needs
// to judge delivery. Every field but Interval and Envelopes is required, and
// each absence is refused by name rather than defaulted.
type SolutionHostBindingReconcilerConfig struct {
	// Source is the delivered desired set, as signed carriers.
	Source SolutionHostBindingSource

	// Verifier is this host's pinned-key check over a carrier. Without one
	// there is no way to construct the verified value admission needs, so a
	// host with no verifier admits nothing rather than everything.
	Verifier *solutionhost.Verifier

	// Coordinate is the host this reconciler answers for.
	Coordinate string

	// Interval between passes. Zero takes the default.
	Interval time.Duration

	// Envelopes is where the administrator's approval — the ceiling a module's
	// requested authority is intersected with — is read from on every pass,
	// so a narrowing takes effect at the next pass with no restart.
	//
	// Optional administrative narrowing for the existing installation surface.
	// With no revision written, the applied signed manifest is the ceiling.
	Envelopes ModuleEnvelopeSource
}

// NewSolutionHostBindingReconciler builds the reconciler for one host
// coordinate. The coordinate is required: core refuses a manifest that targets
// another host, and a host that did not know its own coordinate would reconcile
// whatever it was handed.
func NewSolutionHostBindingReconciler(
	service *Service, config SolutionHostBindingReconcilerConfig,
) (*SolutionHostBindingReconciler, error) {
	if service == nil || config.Source == nil {
		return nil, errors.New("solution host binding reconciler requires a service and a source")
	}
	if config.Verifier == nil {
		return nil, errors.New("solution host binding reconciler requires a pinned-key verifier; a host that cannot verify a carrier must admit nothing, not everything")
	}
	if config.Coordinate == "" {
		return nil, errors.New("solution host binding reconciler requires this host's coordinate")
	}
	interval := config.Interval
	if interval <= 0 {
		interval = SolutionHostBindingReconcileInterval
	}
	reconciler := &SolutionHostBindingReconciler{
		service:    service,
		source:     config.Source,
		coordinate: config.Coordinate,
		verifier:   config.Verifier,
		interval:   interval,
		now:        func() time.Time { return time.Now().UTC() },
		trigger:    make(chan struct{}, 1),
	}
	reconciler.modulePrincipals = &ModulePrincipalReconciler{reconciler: reconciler, envelopes: config.Envelopes}
	return reconciler, nil
}

// RunOnce reads the inbox and reconciles one pass.
//
// The pass is ordered so that a refusal can never disturb what is running.
// Everything is read and judged before anything is written: the inbox, then the
// durable applied state, then core's verdict on each delivered manifest. Only
// then is desired state recorded — for every delivered binding, admitted or
// not, so a withheld generation and its reason are visible — and only then is
// an admitted generation applied.
func (r *SolutionHostBindingReconciler) RunOnce(ctx context.Context) error {
	documents, err := r.source.Documents(ctx)
	if err != nil {
		// An unreadable inbox is not an empty desired set. Nothing is recorded
		// and nothing is applied: the last generation that passed keeps running.
		return fmt.Errorf("read delivered solution host bindings: %w", err)
	}

	verified, unparsed := verifySolutionHostBindingDocuments(ctx, r.verifier, documents)

	records, err := r.service.ListSolutionHostBindings(ctx)
	if err != nil {
		return err
	}
	admission := admitSolutionHostBindings(r.coordinate, r.service.moduleNamespaces, records, verified)

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
		settled[accepted.Document.Binding] = accepted.Decision == solutionhost.DecisionReplay
	}
	// A binding whose manifest asked for authority the module registry could not
	// grant is a DEPLOYMENT DEFECT — the envelope withholding a binding the
	// manifest asks for — and it belongs on that binding's own row beside the
	// generation it applied, not only in this pass's log, where it was a WARN
	// among boot noise nothing reported (issue #1165).
	//
	// The reason is the PREVIOUS pass's, and deliberately so: the registry is
	// derived last, after the applies whose manifests it reads, while desired
	// state is recorded first. Recording it through the write that is already
	// made per binding costs one interval of lag on a reason only an operator
	// can act on; a second write at the end of the pass would instead clear
	// pending_since and set it again every pass, so a defect stuck for a day
	// would read as one that started seconds ago — the reset
	// recordSolutionHostBindingDesired documents refusing.
	withheldAuthority := r.modulePrincipals.WithheldAuthorityReasons()
	var failures []error
	for _, one := range verified {
		document := one.Document
		if err := r.service.recordSolutionHostBindingDesired(
			ctx, document, admission.Withheld[document.Binding], withheldAuthority[document.Binding],
			settled[document.Binding], r.now(),
		); err != nil {
			failures = append(failures, fmt.Errorf("record desired generation for binding %q: %w", document.Binding, err))
		}
	}

	for _, applied := range orderSolutionHostBindingApplies(admission.Accepted, records) {
		if applied.Decision != solutionhost.DecisionAccept {
			// DecisionReplay: this exact generation is already applied. A host
			// re-reads its inbox every pass, so this is the normal answer and
			// not an error. Desired state was recorded above; there is nothing
			// to change.
			continue
		}
		if err := r.service.applySolutionHostBinding(ctx, applied.Delivered, r.coordinate, r.now()); err != nil {
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
			continue
		}
		// The applied generation is composed in the authority core at once,
		// whichever path delivered it: a solution the host's delivery endpoint
		// took is then one the core installs and the host interfaces decide.
		if err := r.service.composeAppliedBinding(ctx, applied.Document.Binding); err != nil {
			failures = append(failures, fmt.Errorf("compose binding %q generation %d in the authority core: %w",
				applied.Document.Binding, applied.Document.Generation, err))
		}
	}

	// THE MODULE REGISTRY, last: it reads the manifests this pass just
	// applied, so deriving it before the applies would serve the previous
	// generation's workloads and authority for one whole interval.
	if r.modulePrincipals != nil {
		if err := r.modulePrincipals.RunOnce(ctx); err != nil {
			failures = append(failures, fmt.Errorf("derive the module principal registry: %w", err))
		}
	}

	// What waits on delivery runs last, once this pass's targets are open and
	// the registry that decides who may install them is derived.
	if r.afterPass != nil {
		if err := r.afterPass(ctx); err != nil {
			failures = append(failures, err)
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

// AfterEachPass sets what runs at the end of every pass: work that waits on a
// delivered target being installable, such as a fixture's declared
// installations. Its refusal is reported with the pass's. Boot wiring, before
// Start.
func (r *SolutionHostBindingReconciler) AfterEachPass(run func(context.Context) error) {
	r.afterPass = run
}

// Trigger asks for a pass now instead of at the next tick, without waiting for
// it. A delivery and an envelope write call it, so what they change is in force
// within one pass rather than one interval. Never blocks: a request made while
// one is already pending is satisfied by that one.
func (r *SolutionHostBindingReconciler) Trigger() {
	if r == nil || r.trigger == nil {
		return
	}
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// ModulePrincipals is the reconciler that derives the module registry, exposed
// so a caller can run or inspect that one step.
func (r *SolutionHostBindingReconciler) ModulePrincipals() *ModulePrincipalReconciler {
	if r == nil {
		return nil
	}
	return r.modulePrincipals
}

// Start runs a pass now and then on every interval until Shutdown, and also
// whenever Trigger asks. A failing pass is logged and the loop continues: the
// inbox is re-read every interval, so a transient failure resolves itself and a
// persistent one keeps saying so.
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
			case <-r.trigger:
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
// distinguished in what is recorded: not a carrier, a carrier whose envelope does
// not verify, or a verified payload core will not read. An operator needs to
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
	ctx context.Context, verifier *solutionhost.Verifier, documents []SolutionHostBindingDocument,
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
		document, err := one.Manifest()
		if err != nil {
			// core read these same bytes inside VerifyDelivered, so this is
			// unreachable by construction rather than merely unlikely. It is
			// still handled as a refusal: a host that cannot read back what it
			// just verified must admit nothing from it.
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

// deliveredSolutionHostBinding pairs an attested carrier with the manifest
// re-derived from its attested bytes, derived ONCE per pass. The apply
// re-derives its own from the carrier, so this copy is what the pass READS and
// cannot become what the host applies.
type deliveredSolutionHostBinding struct {
	Delivered *solutionhost.Delivered
	Document  *solutionhost.ModuleManifest
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
		return candidate.Document.Alias != current
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

// solutionHostBindingRegistryKey resolves the registry record a live manifest
// reconciles into.
//
// The key is the manifest's route alias, because the alias is what this host
// routes on: it is the <id> in /solutions/<id>/* and the key the frontend loads
// a remote under. The binding ID is deliberately not the key — it identifies a
// deployment instance, and a second instance of the same solution inherits
// nothing from the first, alias included.
func solutionHostBindingRegistryKey(document *solutionhost.ModuleManifest) (string, error) {
	alias := document.Alias
	if !solutionIDPattern.MatchString(alias) || len(alias) > maxSolutionIDLength {
		return "", fmt.Errorf("%w: binding %q declares route %q, which is not one lowercase path segment",
			ErrSolutionHostBindingRouteNotAddressable, document.Binding, alias)
	}
	return alias, nil
}
