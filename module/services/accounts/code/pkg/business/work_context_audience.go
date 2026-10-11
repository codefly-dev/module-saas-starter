package business

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// THE HOST'S AUDIENCE VOCABULARY (issue #952).
//
// An audience is what makes two capabilities signed by one key
// non-interchangeable, so it decides which consumer a capability is good at. It
// was free text: any caller could mint under any string, and a per-installation
// `allowed_audiences` list could name one nothing on this host serves.
//
// The vocabulary is now a CLOSED SET, and it is DERIVED rather than written down:
//
//  1. ModuleCapabilitiesAudience — the one consumer a composed module's own
//     capability is good for.
//  2. `solution:<binding-id>` for every DECLARED, NON-TOMBSTONED binding.
//  3. The PREFIX of every declared module principal, because a forwarded viewer
//     Work Context names the module it was minted for in its audience and the
//     content-read surface resolves it AS a module prefix
//     (ModuleContentResources, read by delegated_record_access.go and
//     readable_sources.go). This member is derived from the composition's
//     MODULE_PRINCIPALS registry, so it is no more free text than the other two —
//     a deployment declares it, no caller asks for it. **It is a member the
//     three-clause ruling did not name**, and it is here because leaving it out
//     would make every such ceiling entry dead and take the module content-read
//     surface down; flagged for confirmation rather than quietly widened.
//  4. Nothing else. The gateway's own name is never an audience: a capability
//     addressed to the forwarding hop is one the hop would consume rather than
//     forward.
//
// Derived from delivered presence is the whole point: **the set changes only by
// delivery**. Nobody adds an audience by asking for one, a withdrawal removes it
// with the binding's tombstone, and there is no table an operator edits to widen
// it. The binding id is the key rather than the route alias for the same reason
// the boundary derivation uses it — an alias is reusable, and an audience that
// survived a withdrawal would let a replacement under the same alias be addressed
// as its predecessor.

const (
	// SolutionAudiencePrefix prefixes a declared solution binding's audience. It
	// is a prefix rather than the bare binding id so that a solution audience can
	// never collide with the module one or with a future kind's.
	SolutionAudiencePrefix = "solution:"

	// ModuleCapabilitiesAudience is the module capability surface's audience. It
	// is stated here, beside the set it belongs to, and the adapters' long-standing
	// ModuleWorkContextAudience constant is pinned to it by a test — two spellings
	// of one audience is exactly the drift this set exists to prevent.
	ModuleCapabilitiesAudience = "module-capabilities"
)

// SolutionAudience is the audience of a declared solution binding.
func SolutionAudience(bindingID string) string {
	return SolutionAudiencePrefix + bindingID
}

// ErrAudienceNotInHostVocabulary is returned for an audience outside the closed
// set. It is a refusal that NAMES the value, because the whole failure mode this
// replaces was a string nobody could trace to a consumer.
var ErrAudienceNotInHostVocabulary = fmt.Errorf("audience is not one this host serves")

// DeclaredSolutionBindingStore reads the binding ids the audience set is derived
// from. Separate from SolutionRuntimeBoundarySeedStore because the two want
// DIFFERENT rows: the boundary check includes tombstones, since a withdrawn
// solution's runs may still be executing and their boundaries still have to be
// protected, while an audience must not survive the withdrawal that took the
// consumer away.
type DeclaredSolutionBindingStore interface {
	// LiveDeclaredSolutionBindingIDs returns the binding id of every declared,
	// non-tombstoned registration.
	LiveDeclaredSolutionBindingIDs(ctx context.Context) ([]string, error)
}

// HostAudienceCacheBound is how long a resolved set of declared solution bindings
// may be served before it is re-read.
//
// It is the SAME bound the gateway reads the same registry under, and that is
// deliberate: the host and the edge must agree on how long a withdrawal may take
// to reach a decision, and two different numbers would make the answer depend on
// which one a request happened to hit. It is also four times the reconciler's own
// interval (SolutionHostBindingReconcileInterval), so a withdrawal this replica
// applies itself is visible at once through invalidation, and one ANOTHER replica
// applied is visible within the bound.
const HostAudienceCacheBound = 120 * time.Second

// declaredAudienceSnapshot is one resolved read of the declared binding ids, with
// the time it was taken. Stored behind an atomic pointer and never mutated, so a
// reader either sees a whole previous snapshot or a whole newer one.
type declaredAudienceSnapshot struct {
	bindings []string
	at       time.Time
}

// InvalidateHostAudiences drops the cached binding ids, so the next resolution
// re-reads them.
//
// Called by the apply path whenever a declaration changes the declared set — a
// record declared or withdrawn — which is what makes a withdrawal this replica
// performed visible to the very next mint rather than up to the bound later. It is
// called after the write inside the apply transaction, so a transaction that later
// rolls back costs one extra read and never serves a stale set: the conservative
// direction is the only one worth having here.
func (s *Service) InvalidateHostAudiences() {
	if s == nil {
		return
	}
	s.declaredAudienceBindings.Store(nil)
}

// declaredAudienceBindingIDs resolves the declared binding ids, from the cache
// when it is inside the bound and from the control plane when it is not.
//
// Caching THIS half is what keeps the vocabulary off the per-mint hot path. It
// used to open a control-plane transaction on every call — twice per mint, since
// the mint checks the audience and then narrows the actor's ceiling — on a pool
// whose MaxConns is the pgx default and which every other control-plane capability
// shares, including the delivery reconciler. A mint storm therefore queued
// delivery, which is the mechanism that withdraws a compromised solution.
func (s *Service) declaredAudienceBindingIDs(ctx context.Context) ([]string, error) {
	now := time.Now
	if s.audienceClock != nil {
		now = s.audienceClock
	}
	if snapshot := s.declaredAudienceBindings.Load(); snapshot != nil &&
		now().Sub(snapshot.at) < HostAudienceCacheBound {
		return snapshot.bindings, nil
	}
	store, ok := s.store.(DeclaredSolutionBindingStore)
	if !ok {
		// Fail CLOSED and say which capability the deployment is missing. A store
		// that cannot answer must not read as "no solution audiences", which would
		// refuse every solution mint while looking like a vocabulary decision.
		return nil, fmt.Errorf("cannot resolve the host audience vocabulary: store does not read declared solution bindings")
	}
	var bindings []string
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		bindings, err = store.LiveDeclaredSolutionBindingIDs(ctx)
		return err
	}); err != nil {
		// A failed read does NOT replace the snapshot: an outage must not install an
		// empty set that then reads as "this host serves no solutions" for a whole
		// bound. The caller fails closed on the error instead.
		return nil, fmt.Errorf("cannot resolve the host audience vocabulary: %w", err)
	}
	s.declaredAudienceBindings.Store(&declaredAudienceSnapshot{bindings: bindings, at: now()})
	return bindings, nil
}

// HostAudiences is the closed set.
//
// The solution half is served from a snapshot bounded by HostAudienceCacheBound
// and invalidated by the apply path; the module half is composed fresh from the
// in-memory principal registry on every call, so declaring a module takes effect
// at once and needs no invalidation hook.
func (s *Service) HostAudiences(ctx context.Context) (map[string]struct{}, error) {
	if s == nil || s.store == nil {
		// A deployment with no service wired cannot answer what it serves, and must
		// not answer "nothing" either: every mint refuses, loudly, rather than
		// panicking on a nil store three frames deeper.
		return nil, fmt.Errorf("cannot resolve the host audience vocabulary: accounts service is not configured")
	}
	bindings, err := s.declaredAudienceBindingIDs(ctx)
	if err != nil {
		return nil, err
	}
	modules := s.declaredModules()
	set := make(map[string]struct{}, len(bindings)+len(modules)+1)
	set[ModuleCapabilitiesAudience] = struct{}{}
	for _, binding := range bindings {
		if binding = strings.TrimSpace(binding); binding != "" {
			set[SolutionAudience(binding)] = struct{}{}
		}
	}
	// A declared module's own prefix. The registry is keyed by principal id, and
	// the audience is the PREFIX that id is derived from — the value the
	// content-read surface looks a module up by.
	for _, grant := range modules {
		if prefix := strings.TrimSpace(grant.Prefix); prefix != "" {
			set[prefix] = struct{}{}
		}
	}
	return set, nil
}

// RequireHostAudience refuses an audience outside the closed set, by name.
//
// An empty audience is refused first and separately: it is not "a value this host
// does not serve", it is the absence of the field that decides which consumer a
// capability is good at, and an operator reading the two refusals does different
// things about them.
func (s *Service) RequireHostAudience(ctx context.Context, audience string) error {
	if strings.TrimSpace(audience) == "" {
		return fmt.Errorf("%w: an audience is required, and a capability with no audience is good at whichever consumer accepts it",
			ErrAudienceNotInHostVocabulary)
	}
	set, err := s.HostAudiences(ctx)
	if err != nil {
		return err
	}
	if _, ok := set[audience]; ok {
		return nil
	}
	return fmt.Errorf("%w: %q (%s)", ErrAudienceNotInHostVocabulary, audience, hostAudienceShapes)
}

// hostAudienceShapes names what this host serves, for a refusal. It is a CONSTANT:
// the same sentence whatever the set contains.
//
// An earlier version counted the members — "%d declared solution binding
// audience(s)" — on the reasoning that counting is not listing. That was wrong in
// its own terms: a count is information about the same secret, so one refused mint
// told any caller how many solutions this deployment runs, and repeating the probe
// told them when that changed, which is to say when a solution was installed or
// withdrawn. The registration surface takes constant-time care never to reveal
// which modules a composition declares; a cardinality channel beside it gives that
// away more slowly and just as surely.
//
// Deleting the counts also deletes the arithmetic that produced them. It derived
// one cardinality by subtracting another from the deduplicated set, so a module
// whose prefix WAS "module-capabilities" reported zero module prefixes when there
// was one, and a prefix beginning "solution:" was counted as a solution.
const hostAudienceShapes = "this host serves " + `"` + ModuleCapabilitiesAudience + `"` +
	", an audience of the form \"" + SolutionAudiencePrefix + "<binding-id>\" for a declared solution binding" +
	", or a declared module prefix"

// RequireHostAudiences refuses a LIST, naming every entry outside the set.
//
// This is the write-time half of the `allowed_audiences` rule. It reports every
// offending entry rather than the first, because an operator fixing a ceiling is
// editing one list and wants one answer about it.
func (s *Service) RequireHostAudiences(ctx context.Context, audiences []string) error {
	if len(audiences) == 0 {
		return nil
	}
	set, err := s.HostAudiences(ctx)
	if err != nil {
		return err
	}
	var rejected []string
	for _, audience := range audiences {
		if strings.TrimSpace(audience) == "" {
			rejected = append(rejected, `""`)
			continue
		}
		if _, ok := set[audience]; !ok {
			rejected = append(rejected, fmt.Sprintf("%q", audience))
		}
	}
	if len(rejected) == 0 {
		return nil
	}
	sort.Strings(rejected)
	return fmt.Errorf("%w: %s (%s)",
		ErrAudienceNotInHostVocabulary, strings.Join(rejected, ", "), hostAudienceShapes)
}

// LiveHostAudiences narrows a stored ceiling to the audiences the host still
// serves. It is the READ-time half of the rule.
//
// An entry that has left the set is DEAD, not granted: a solution withdrawn after
// an installation named its audience leaves a ceiling entry that no longer
// describes a consumer, and treating it as live would let a capability be minted
// for an audience delivery has taken away. Write-time validation alone cannot
// cover this — the set shrinks by delivery, long after any write.
//
// Returning the narrowed list rather than an error is deliberate: a ceiling with
// one dead entry and two live ones should still allow the two.
func (s *Service) LiveHostAudiences(ctx context.Context, audiences []string) ([]string, error) {
	if len(audiences) == 0 {
		return nil, nil
	}
	set, err := s.HostAudiences(ctx)
	if err != nil {
		return nil, err
	}
	live := make([]string, 0, len(audiences))
	for _, audience := range audiences {
		if _, ok := set[audience]; ok {
			live = append(live, audience)
		}
	}
	return live, nil
}

// SetAudienceClockForTest moves the vocabulary cache's clock. Test-only, named so,
// and the only way to prove the bound expires without sleeping it out.
func (s *Service) SetAudienceClockForTest(now func() time.Time) { s.audienceClock = now }
