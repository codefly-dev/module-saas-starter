package business

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/core/wool"
	"github.com/codefly-dev/core/workcontext"
	"google.golang.org/protobuf/proto"

	compositionv1 "accounts/pkg/gen/host/composition/v1"
)

var ErrDeliveredMethodUnknown = errors.New("delivered method is unknown")

// SetAuthorityDocumentVerifier wires the same pinned keys as core delivery.
func (s *Service) SetAuthorityDocumentVerifier(verify func([]byte, []byte) (string, error)) {
	s.authorityDocumentVerifier = verify
}

// SetCompositionReconcile wires synchronous application to the existing reconciler.
func (s *Service) SetCompositionReconcile(run func(context.Context) error) {
	s.compositionReconcile = run
}

// ApplyComposition persists the signed pair, then waits for the existing apply
// owner. An inbox receipt alone never acknowledges an applied generation.
func (s *Service) ApplyComposition(ctx context.Context, req *compositionv1.ApplyManifestRequest) (*compositionv1.ApplyManifestResponse, error) {
	if req.GetCarrier() == nil {
		return nil, fmt.Errorf("%w: composition requires a signed carrier; unsigned manifest sketches are refused", ErrSolutionDeliveryUnattested)
	}
	if s.compositionReconcile == nil {
		return nil, fmt.Errorf("%w: composition reconciler is not wired", ErrSolutionDeliveryHostState)
	}
	// A delivery the authority core would refuse to compose is refused before
	// anything is recorded, so accounts' record and the core's never part.
	if err := precheckCoreModule(req); err != nil {
		return nil, coreDeliveryError(err)
	}
	raw, err := solutionhost.MarshalSigned(&solutionhost.Signed{Document: req.GetCarrier().GetDocument(), Envelope: req.GetCarrier().GetEnvelope()})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
	}
	_, record, err := s.receiveSolutionDelivery(ctx, raw, "Bearer "+req.GetCredential(), req.GetAuthority())
	if err != nil {
		return nil, err
	}
	if err := s.compositionReconcile(ctx); err != nil {
		// A pass also reconciles other bindings and fixture installations. Its
		// aggregate error is operator evidence, never this carrier's outcome.
		wool.Get(ctx).In("business.ApplyComposition").Warn("composition reconcile pass failed", wool.ErrField(err))
	}
	records, err := s.ListSolutionHostBindings(ctx)
	if err != nil {
		wool.Get(ctx).In("business.ApplyComposition").Warn("composition applied state could not be read", wool.ErrField(err))
		return nil, fmt.Errorf("%w: binding %s generation %d: applied state could not be read", ErrSolutionDeliveryHostState, record.Binding, record.Generation)
	}
	for _, row := range records {
		if row.BindingID != record.Binding {
			continue
		}
		if row.Applied == nil || row.Applied.Generation != record.Generation || row.Applied.Digest != record.ContentHash {
			reason := row.PendingReason
			if reason == "" {
				reason = "no matching applied generation and digest were recorded"
			}
			return nil, fmt.Errorf("%w: binding %s generation %d is not applied: %s", ErrSolutionDeliveryConflict, record.Binding, record.Generation, reason)
		}
		manifest, err := row.AppliedManifest()
		if err != nil {
			return nil, err
		}
		// The applied, verified delivery is what the core composes.
		ids, err := s.applyCoreModule(ctx, manifest, req.GetCarrier().GetDocument(), req.GetAuthority())
		if err != nil {
			return nil, coreDeliveryError(err)
		}
		principal := manifest.Alias
		if manifest.Authority != nil && manifest.Authority.Principal != "" {
			principal = manifest.Authority.Principal
		}
		return &compositionv1.ApplyManifestResponse{Principal: principal, Generation: record.Generation, InstallationIds: ids}, nil
	}
	return nil, fmt.Errorf("%w: binding %s has no applied record", ErrSolutionDeliveryHostState, record.Binding)
}

func (s *Service) verifyAuthorityDocument(signed *compositionv1.SignedManifestV1, core *solutionhost.Delivered) (*compositionv1.AuthorityManifestV1, error) {
	if signed == nil {
		return nil, nil
	}
	if s.authorityDocumentVerifier == nil {
		return nil, fmt.Errorf("%w: authority document verifier is not wired", ErrSolutionDeliveryUnattested)
	}
	signer, err := s.authorityDocumentVerifier(signed.GetDocument(), signed.GetEnvelope())
	if err != nil {
		return nil, fmt.Errorf("%w: authority document: %w", ErrSolutionDeliveryUnattested, err)
	}
	if signer != core.Signer().KeyID {
		return nil, fmt.Errorf("%w: authority document and core manifest have different signers", ErrSolutionDeliveryUnattested)
	}
	manifest, err := core.Manifest()
	if err != nil {
		return nil, err
	}
	doc := &compositionv1.AuthorityManifestV1{}
	if err := proto.Unmarshal(signed.GetDocument(), doc); err != nil {
		return nil, fmt.Errorf("%w: authority document: %w", ErrSolutionDeliveryMalformed, err)
	}
	if doc.GetModule() != manifest.Alias || doc.GetGeneration() != manifest.Generation || doc.GetCoreManifestDigest() != delivery.Digest(core.Payload()) {
		return nil, fmt.Errorf("%w: authority document does not name core manifest %s generation %d and its digest", ErrSolutionDeliveryUnattested, manifest.Alias, manifest.Generation)
	}
	if manifest.Removed && (len(doc.GetMethods())+len(doc.GetRoles())+len(doc.GetScopeCeilings()) > 0) {
		return nil, fmt.Errorf("%w: withdrawal carries live authority declarations", ErrSolutionDeliveryMalformed)
	}
	seen := map[string]bool{}
	for _, method := range doc.GetMethods() {
		if seen[method.GetMethod()] {
			return nil, fmt.Errorf("%w: duplicate method %s", ErrSolutionDeliveryMalformed, method.GetMethod())
		}
		seen[method.GetMethod()] = true
		requires := map[string][]string{}
		for _, scope := range method.GetRequires() {
			if len(scope.GetResourceIds()) != 0 {
				return nil, fmt.Errorf("%w: %s's requirements name selected resources instead of a whole kind", ErrSolutionDeliveryMalformed, method.GetMethod())
			}
			requires[scope.GetResourceKind()] = append(requires[scope.GetResourceKind()], scope.GetActions()...)
		}
		if err := delivery.ValidateMethodPolicy(method.GetMethod(), method.GetAnyCheckedCaller(), requires, false); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
		}
	}
	for _, scope := range doc.GetScopeCeilings() {
		if manifest.Authority == nil || !slices.Contains(manifest.Authority.ResourceKinds, scope.GetResourceKind()) || len(scope.GetActions()) == 0 || len(scope.GetResourceIds()) > 0 {
			return nil, fmt.Errorf("%w: authority ceiling %s is not a declared whole kind with actions", ErrSolutionDeliveryMalformed, scope.GetResourceKind())
		}
	}
	if err := validateAuthorityRoles(manifest, doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// validateAuthorityRoles keeps signed person roles within served authority and
// explicit invoke/lookup bindings. Headless authority cannot become a role.
func validateAuthorityRoles(manifest *solutionhost.ModuleManifest, doc *compositionv1.AuthorityManifestV1) error {
	var allowed []*basev0.WorkScopeV1
	ceiled := map[string]bool{}
	for _, scope := range doc.GetScopeCeilings() {
		ceiled[scope.GetResourceKind()] = true
		allowed = append(allowed, &basev0.WorkScopeV1{ResourceKind: scope.GetResourceKind(), Actions: scope.GetActions()})
	}
	var served []string
	if manifest.Authority != nil {
		served = manifest.Authority.ResourceKinds
		for _, binding := range manifest.Authority.Bindings {
			for _, operation := range []string{"invoke", "lookup"} {
				for _, scope := range scopesOf(binding.Operations[operation]) {
					allowed = append(allowed, &basev0.WorkScopeV1{ResourceKind: scope.ResourceKind, Actions: scope.Actions})
				}
			}
		}
	}
	seen := map[string]bool{}
	for _, role := range doc.GetRoles() {
		if role.GetName() == "" || seen[role.GetName()] || len(role.GetScopes()) == 0 {
			return fmt.Errorf("%w: role %q is unnamed, duplicated or grants nothing", ErrSolutionDeliveryRefused, role.GetName())
		}
		seen[role.GetName()] = true
		for _, scope := range role.GetScopes() {
			// A served kind with no signed action ceiling permits the role's
			// actions; an explicit ceiling and all consumed kinds are bounded.
			unbounded := slices.Contains(served, scope.GetResourceKind()) && !ceiled[scope.GetResourceKind()]
			asked := &basev0.WorkScopeV1{ResourceKind: scope.GetResourceKind(), Actions: scope.GetActions(), ResourceIds: scope.GetResourceIds()}
			if len(asked.Actions) == 0 || (!unbounded && !workcontext.ScopeContained(asked, allowed)) {
				return fmt.Errorf("%w: role %q of %s grants beyond its served or invoke/lookup authority", ErrSolutionDeliveryRefused, role.GetName(), manifest.Alias)
			}
		}
	}
	return nil
}

// CheckDeliveredMethod reads the exact applied generation, re-verifying both
// signatures with current pins. A pending generation cannot change live policy.
func (s *Service) CheckDeliveredMethod(ctx context.Context, audience, method string, held []*basev0.WorkScopeV1) error {
	doc, _, err := s.appliedAuthorityDocument(ctx, audience)
	if err != nil {
		return err
	}
	if doc == nil {
		return fmt.Errorf("%s's delivery declares no method policy, so %s is refused", audience, method)
	}
	return checkMethodPolicy(doc, audience, method, held)
}

// appliedAuthorityDocument is the signed authority document delivered with the
// applied generation of audience's manifest, both signatures re-verified with
// current pins, beside that manifest. A principal with no applied delivery, or
// one delivered without an authority document, answers nil and no error.
func (s *Service) appliedAuthorityDocument(ctx context.Context, audience string) (*compositionv1.AuthorityManifestV1, *solutionhost.ModuleManifest, error) {
	doc, manifest, err := s.appliedAuthorityDocumentOf(ctx, audience, func(_ string, manifest *solutionhost.ModuleManifest) bool {
		return manifest.Authority.Principal == audience
	})
	if errors.Is(err, errNoAuthorityDocument) {
		return nil, nil, nil
	}
	return doc, manifest, err
}

// errNoAuthorityDocument is an applied delivery that carries no signed
// authority document, or no applied delivery at all.
var errNoAuthorityDocument = errors.New("no applied authority document")

// appliedAuthorityDocumentOf is appliedAuthorityDocument for the first applied
// binding is chooses; audience names it in a refusal. Finding no document is
// errNoAuthorityDocument, naming why.
//
// A generation's document is verified once: the outcome is kept per binding
// for the applied generation and digest it was verified at, so a check reads
// the applied rows and nothing else until a pass applies another generation.
// A row whose applied manifest does not parse is passed over rather than
// failing every other binding's answer; when no row is chosen, that failure is
// the answer, since the unreadable row may have been the one asked about.
func (s *Service) appliedAuthorityDocumentOf(ctx context.Context, audience string, is func(binding string, manifest *solutionhost.ModuleManifest) bool) (*compositionv1.AuthorityManifestV1, *solutionhost.ModuleManifest, error) {
	if s.deliveryStore == nil || s.deliveryVerifier == nil {
		return nil, nil, fmt.Errorf("%w: this host has no signed delivery wired", errNoAuthorityDocument)
	}
	records, err := s.ListSolutionHostBindings(ctx)
	if err != nil {
		return nil, nil, err
	}
	var unreadable error
	for _, row := range records {
		manifest, err := row.AppliedManifest()
		if err != nil {
			unreadable = cmp.Or(unreadable, fmt.Errorf("binding %s's applied manifest does not parse: %w", row.BindingID, err))
			continue
		}
		if manifest == nil || manifest.Removed || manifest.Authority == nil || !is(row.BindingID, manifest) {
			continue
		}
		verified, ok := s.authorityDocuments.get(row.BindingID, row.Applied)
		if !ok {
			if verified, err = s.verifyAppliedAuthorityDocument(ctx, audience, row, manifest); err != nil {
				return nil, nil, err
			}
			s.authorityDocuments.put(row.BindingID, verified)
		}
		if verified.doc == nil {
			return nil, nil, fmt.Errorf("%w: %s's applied generation %d was delivered without its authority document", errNoAuthorityDocument, audience, manifest.Generation)
		}
		return verified.doc, manifest, nil
	}
	if unreadable != nil {
		return nil, nil, unreadable
	}
	return nil, nil, fmt.Errorf("%w: no applied delivery of %s declares its authority", errNoAuthorityDocument, audience)
}

// verifyAppliedAuthorityDocument reads row's applied generation from the
// inbox and verifies both signatures with the pinned keys: the outcome kept
// for that generation, with no document when it was delivered without one.
func (s *Service) verifyAppliedAuthorityDocument(ctx context.Context, audience string, row *SolutionHostBindingRecord, manifest *solutionhost.ModuleManifest) (verifiedAuthorityDocument, error) {
	var record *SolutionDeliveryRecord
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var e error
		record, e = s.deliveryStore.ReadDeliveredDocument(ctx, row.BindingID, manifest.Generation)
		return e
	})
	if err != nil {
		return verifiedAuthorityDocument{}, err
	}
	if record == nil {
		return verifiedAuthorityDocument{}, fmt.Errorf("%w: %s's applied generation %d has no inbox record", errNoAuthorityDocument, audience, manifest.Generation)
	}
	core, err := solutionhost.VerifyDelivered(ctx, carrierOf(record), s.deliveryVerifier)
	if err != nil {
		return verifiedAuthorityDocument{}, fmt.Errorf("%s delivery does not verify: %w", audience, err)
	}
	if delivery.Digest(core.Payload()) != row.Applied.Digest {
		return verifiedAuthorityDocument{}, fmt.Errorf("%s applied delivery digest differs from its inbox", audience)
	}
	verified := verifiedAuthorityDocument{generation: row.Applied.Generation, digest: row.Applied.Digest}
	if record.AuthorityDocument == nil {
		return verified, nil
	}
	if verified.doc, err = s.verifyAuthorityDocument(&compositionv1.SignedManifestV1{Document: record.AuthorityDocument, Envelope: record.AuthorityEnvelope}, core); err != nil {
		return verifiedAuthorityDocument{}, err
	}
	return verified, nil
}

func checkMethodPolicy(doc *compositionv1.AuthorityManifestV1, audience, method string, held []*basev0.WorkScopeV1) error {
	var selected *compositionv1.MethodPolicyV1
	for _, policy := range doc.GetMethods() {
		if policy.GetMethod() == method || strings.TrimPrefix(method, "POST ") == policy.GetMethod() {
			selected = policy
			break
		}
	}
	if selected == nil {
		for _, policy := range doc.GetMethods() {
			if !methodRouteMatches(policy.GetMethod(), method) {
				continue
			}
			if selected != nil {
				return fmt.Errorf("%s: %s matches more than one declared route", audience, method)
			}
			selected = policy
		}
	}
	if selected == nil {
		return fmt.Errorf("%w: %s serves no method %s", ErrDeliveredMethodUnknown, audience, method)
	}
	if !selected.GetAnyCheckedCaller() && len(selected.GetRequires()) == 0 {
		return fmt.Errorf("%s declares no policy for %s", audience, method)
	}
	if selected.GetAnyCheckedCaller() {
		return nil
	}
	// Method admission checks kind and action eligibility. The module checks
	// exact resources against the original scopes returned by Check; admitting
	// the method must neither require nor grant whole-kind resource authority.
	eligibility := make([]*basev0.WorkScopeV1, 0, len(held))
	for _, scope := range held {
		eligibility = append(eligibility, &basev0.WorkScopeV1{ResourceKind: scope.GetResourceKind(), Actions: scope.GetActions()})
	}
	for _, scope := range selected.GetRequires() {
		required := &basev0.WorkScopeV1{ResourceKind: scope.GetResourceKind(), Actions: scope.GetActions()}
		if !workcontext.ScopeContained(required, eligibility) {
			return fmt.Errorf("%s requires %s:%s for %s, which the call does not hold", audience, scope.GetResourceKind(), strings.Join(scope.GetActions(), ","), method)
		}
	}
	return nil
}

func methodRouteMatches(declared, method string) bool {
	verb, template, ok := strings.Cut(declared, " ")
	if !ok {
		return false
	}
	actualVerb, path, ok := strings.Cut(method, " ")
	if !ok || verb != actualVerb {
		return false
	}
	want, got := strings.Split(template, "/"), strings.Split(path, "/")
	// The published HTTP contract uses the reference authority's route grammar:
	// {name} within a segment, or a final {name=**} taking the remaining path,
	// optionally followed by a literal custom verb such as :settle.
	if last := want[len(want)-1]; strings.Contains(last, "=**}") {
		open, end := strings.IndexByte(last, '{'), strings.Index(last, "=**}")
		if open < 0 || end <= open+1 || len(got) < len(want) {
			return false
		}
		prefix, suffix := last[:open], last[end+4:]
		rest := strings.Join(got[len(want)-1:], "/")
		if !strings.HasPrefix(rest, prefix) || !strings.HasSuffix(rest, suffix) || len(rest) <= len(prefix)+len(suffix) {
			return false
		}
		want, got = want[:len(want)-1], got[:len(want)-1]
	}
	if len(want) != len(got) {
		return false
	}
	for i, part := range want {
		if !methodSegmentMatches(part, got[i]) {
			return false
		}
	}
	return true
}

func methodSegmentMatches(template, segment string) bool {
	open := strings.IndexByte(template, '{')
	if open < 0 {
		return template == segment
	}
	end := strings.IndexByte(template[open:], '}')
	if end <= 1 || !strings.HasPrefix(segment, template[:open]) || strings.ContainsAny(template[open+1:open+end], "={}") {
		return false
	}
	rest, tail := segment[open:], template[open+end+1:]
	for i := 1; i <= len(rest); i++ {
		if methodSegmentMatches(tail, rest[i:]) {
			return true
		}
	}
	return false
}
