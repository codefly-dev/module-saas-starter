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
// A solution used to become present on this host by registering itself: a
// runtime heartbeats, and the host learns of it as a side effect of a process
// being up. A SolutionHostBinding is the opposite record — delivery declares
// which solution runs on this host at which generation, and the host reconciles
// towards it. Nothing here renders or signs a document; core owns the document
// and its admission rules (github.com/codefly-dev/core/solutionhost), and this
// file is the host half: read the mount, ask core about the whole desired set,
// and reconcile what core admits into the durable registry that already exists.
//
// Three facts are kept apart, because an operator's first question is which of
// them is wrong:
//
//   - DESIRED is the newest generation delivery has shown this host, admitted or
//     not, with the reason it was not.
//   - APPLIED is the generation this host reconciled, and the registry record it
//     reconciled into. It is durable, so a restart resumes instead of deriving
//     the answer again, and two replicas reading the same mount converge on it
//     without applying it twice.
//   - OBSERVED is not here. It is the lease and the endpoints on
//     solution_registrations, reported by the runtime, and it is what tells a
//     declared-but-unhealthy solution from one that was never declared.
//
// # The mixed window
//
// Self-registration is not removed here — that is the next step. Until it is,
// both paths write the same registry, so the rule that makes this safe to land
// is asymmetric and is enforced in solution_registry.go, on the row a heartbeat
// already locks: a heartbeat for a DECLARED record may only refresh what the
// declaration does not own, and a heartbeat for an UNDECLARED one behaves
// exactly as it did before. A record becomes declared when a generation APPLIES,
// never when a document merely arrives, so a document that has not passed every
// check cannot take a working self-registered solution offline.

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
// that could not be one. It matches PutSolutionRegistrationRequest.solution_id.
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
	Data   []byte
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
	service  *Service
	source   SolutionHostBindingSource
	host     solutionhost.Host
	interval time.Duration
	now      func() time.Time

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewSolutionHostBindingReconciler builds the reconciler for one host
// coordinate. The coordinate is required: core refuses a document that targets
// another host, and a host that did not know its own coordinate would reconcile
// whatever it was handed.
func NewSolutionHostBindingReconciler(
	service *Service, source SolutionHostBindingSource, coordinate string, interval time.Duration,
) (*SolutionHostBindingReconciler, error) {
	if service == nil || source == nil {
		return nil, errors.New("solution host binding reconciler requires a service and a source")
	}
	if coordinate == "" {
		return nil, errors.New("solution host binding reconciler requires this host's coordinate")
	}
	if interval <= 0 {
		interval = SolutionHostBindingReconcileInterval
	}
	return &SolutionHostBindingReconciler{
		service: service,
		source:  source,
		host: solutionhost.Host{
			Coordinate: coordinate,
			Reserved:   []string{SolutionHostReservedRouteNamespace},
		},
		interval: interval,
		now:      func() time.Time { return time.Now().UTC() },
	}, nil
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

	parsed, unparsed := parseSolutionHostBindingDocuments(documents)

	records, err := r.service.ListSolutionHostBindings(ctx)
	if err != nil {
		return err
	}
	host := r.host
	host.Applied = appliedStates(records)

	admission := admitSolutionHostBindings(host, parsed)

	// Desired state is recorded for every delivered document before anything is
	// applied, so an operator sees what delivery is showing even when none of it
	// passed. A document that did not parse names no binding it can be recorded
	// against; those are reported to the caller.
	var failures []error
	for _, document := range parsed {
		reason := admission.Withheld[document.Binding]
		if err := r.service.recordSolutionHostBindingDesired(ctx, document, reason, r.now()); err != nil {
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
		if err := r.service.applySolutionHostBinding(ctx, applied.Document, r.host.Coordinate, r.now()); err != nil {
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

// parseSolutionHostBindingDocuments parses every delivered document, keeping the
// ones core accepts as documents and describing the ones it does not.
//
// A document that does not parse is still attributed to a binding ID when it
// names one, so its refusal is exposed rather than only logged. That attribution
// reads the binding field with a NON-strict decode and is used for nothing else:
// admission always runs on solutionhost.Parse's output, so a document that
// cheats the attribution decode cannot be admitted by it.
func parseSolutionHostBindingDocuments(
	documents []SolutionHostBindingDocument,
) ([]*solutionhost.SolutionHostBinding, []unparsedSolutionHostBinding) {
	parsed := make([]*solutionhost.SolutionHostBinding, 0, len(documents))
	var problems []unparsedSolutionHostBinding
	for _, delivered := range documents {
		document, err := solutionhost.Parse(delivered.Data)
		if err != nil {
			problems = append(problems, unparsedSolutionHostBinding{
				binding: attributeSolutionHostBinding(delivered.Data),
				err:     fmt.Errorf("delivered solution host binding %s: %w", delivered.Source, err),
			})
			continue
		}
		parsed = append(parsed, document)
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].Binding < parsed[j].Binding })
	return parsed, problems
}

// unparsedSolutionHostBinding is a delivered document core would not parse, and
// the binding it claims to be for.
type unparsedSolutionHostBinding struct {
	binding string
	err     error
}

// attributeSolutionHostBinding reads only the binding ID out of a document that
// solutionhost.Parse refused, so the refusal can be recorded against it. It
// never produces a document and its result is never admitted.
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
