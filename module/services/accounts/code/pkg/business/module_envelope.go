package business

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	modulecontract "github.com/codefly-dev/core/contracts/module"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/core/wool"
)

// The module ENVELOPE: the authority ceiling a platform super_admin writes at
// runtime (the solution lifecycle design, §1 and §3).
//
// WHAT IT REPLACES. The ceiling was a file read once at boot from the trust
// anchor, and the module registry every capability decision reads was
// MODULE_PRINCIPALS, parsed once from the environment. Installing a module,
// narrowing one, or changing what one may touch therefore meant a configuration
// edit and an accounts restart — a static act standing in for what the design
// says is the one dynamic record a static delivery is held against.
//
// WHAT IT HOLDS. Per module principal — keyed by the module's prefix, the name
// its authority document grants to — three things, and nothing else:
//
//   - bindings: the authority unit IDs this host admits for the principal,
//     exactly as delivery names them (<presence binding>:<binding>:<operation>).
//     A document granting a unit that is not listed is OUTSIDE the envelope and
//     grants nothing at all — the core rule that containment is exact.
//   - scope_ceilings: the subject module's own vocabulary this host allows it to
//     declare, held whole.
//   - grant: the ceiling of the registry entry the principal can be given —
//     tenant, cross_tenant, queues, namespaces, external namespaces, resources,
//     read and operation audiences, artifact policies. It carries NO workload:
//     which pods a principal runs as is a delivered fact (the applied presence
//     document), never something an administrator types.
//
// WHAT IT IS NOT. It is not the registry. The registry is DERIVED each
// reconcile pass from the live authority documents inside this ceiling and the
// applied presence they name (module_principal_reconcile.go), so a principal is
// in it only while both halves say so: the envelope allows it AND a signed,
// delivered document asks for it.
//
// ONE CEILING, NOT TWO. Core's ValidateAgainst still decides whether a document
// is inside the ceiling; it is handed the projection of this record for that
// document (authorityEnvelopeFor), so the containment rule a document is held to
// is core's, and the registry derivation then only intersects what is already
// known to be inside.

var (
	// ErrModuleEnvelopeInvalid reports a document that is not a ceiling this
	// host can apply. It is the caller's to fix and is never stored.
	ErrModuleEnvelopeInvalid = errors.New("module envelope is invalid")

	// ErrModuleEnvelopeConflict reports a write whose expected revision is not
	// the current one: another administrator wrote first, or the caller read a
	// stale revision. Nothing was written; re-read and decide again.
	ErrModuleEnvelopeConflict = errors.New("module envelope revision conflict")

	// ErrModuleEnvelopeUnavailable reports a host with no envelope store wired.
	// It answers no question about the ceiling rather than answering "none".
	ErrModuleEnvelopeUnavailable = errors.New("this host has no module envelope store")
)

// maxModuleEnvelopeBytes bounds one envelope document. It is a ceiling for a
// host's modules, not a dataset; a megabyte is far beyond any real one and
// small enough that a careless paste cannot become a memory problem.
const maxModuleEnvelopeBytes = 1 << 20

// ModuleEnvelope is one parsed, validated revision of the ceiling.
type ModuleEnvelope struct {
	// Revision is this ceiling's revision, from 1. An authority document names
	// the revision it was rendered against and is inside only this one.
	Revision uint64
	// ApprovedBuilds are the OCI image manifest digests this host approves.
	ApprovedBuilds []solutionhost.ImageDigest
	// Principals is the ceiling per module principal, keyed by module prefix.
	Principals map[string]ModuleEnvelopePrincipal
}

// ModuleEnvelopePrincipal is the ceiling for one module principal.
type ModuleEnvelopePrincipal struct {
	// Bindings are the authority unit IDs this host admits for the principal.
	Bindings []string
	// ScopeCeilings are the vocabulary entries the subject module may declare.
	ScopeCeilings []solutionhost.ScopeCeiling
	// Grant is the registry entry's ceiling. Prefix is the key it is filed
	// under and Workload is always empty: the workload is derived.
	Grant ModulePrincipalGrant
}

// ModuleEnvelopeRecord is one stored revision: the document as written, and who
// wrote it when.
type ModuleEnvelopeRecord struct {
	Revision  uint64
	Document  []byte
	WrittenBy string
	WrittenAt time.Time
}

// ModuleEnvelopeStore persists the ceiling. Append-only: a revision is written
// once and never changed.
type ModuleEnvelopeStore interface {
	// LatestModuleEnvelopeRecord returns the highest revision, or nil when no
	// envelope has ever been written.
	LatestModuleEnvelopeRecord(ctx context.Context) (*ModuleEnvelopeRecord, error)
	// AppendModuleEnvelopeRecord writes record.Revision only while the current
	// revision is expectedRevision (0 = none written), and returns
	// ErrModuleEnvelopeConflict otherwise. The store fills WrittenAt.
	AppendModuleEnvelopeRecord(ctx context.Context, record *ModuleEnvelopeRecord, expectedRevision uint64) error
}

// ModuleEnvelopeSource is what the reconcilers read the CURRENT ceiling from on
// every pass. Nil with no error means no envelope has been written, which grants
// nothing; an error means the ceiling could not be established, and a reconciler
// then keeps what it last installed rather than reading the failure as "none".
type ModuleEnvelopeSource interface {
	CurrentModuleEnvelope(ctx context.Context) (*ModuleEnvelope, error)
}

// moduleEnvelopeWire is the document's JSON shape.
type moduleEnvelopeWire struct {
	Revision       uint64                                 `json:"revision"`
	ApprovedBuilds []solutionhost.ImageDigest             `json:"approved_builds"`
	Principals     map[string]moduleEnvelopePrincipalWire `json:"principals"`
}

type moduleEnvelopePrincipalWire struct {
	Bindings      []string                    `json:"bindings"`
	ScopeCeilings []solutionhost.ScopeCeiling `json:"scope_ceilings"`
	Grant         moduleEnvelopeGrantWire     `json:"grant"`
}

// moduleEnvelopeGrantWire is a registry entry's ceiling as an administrator
// writes it. It has no `workload` key, and the decoder is strict, so an envelope
// that tries to name the pods a principal runs as is refused by name rather
// than half-applied: the workload is read from delivered presence.
type moduleEnvelopeGrantWire struct {
	ArtifactPolicies   map[string]ExecutableArtifactPolicy `json:"artifact_policies"`
	ReadAudiences      map[string]ModuleReadAudience       `json:"read_audiences"`
	OperationAudiences map[string]ModuleOperationAudience  `json:"operation_audiences"`
	Queues             []string                            `json:"queues"`
	Namespaces         []string                            `json:"namespaces"`
	ExternalNamespaces []string                            `json:"external_namespaces"`
	Resources          []string                            `json:"resources"`
	CrossTenant        bool                                `json:"cross_tenant"`
	Tenant             string                              `json:"tenant"`
}

func (wire moduleEnvelopeGrantWire) grant(prefix string) ModulePrincipalGrant {
	return ModulePrincipalGrant{
		ArtifactPolicies:   wire.ArtifactPolicies,
		ReadAudiences:      wire.ReadAudiences,
		OperationAudiences: wire.OperationAudiences,
		Prefix:             prefix,
		Queues:             wire.Queues,
		Namespaces:         wire.Namespaces,
		ExternalNamespaces: wire.ExternalNamespaces,
		Resources:          wire.Resources,
		CrossTenant:        wire.CrossTenant,
		Tenant:             wire.Tenant,
	}
}

// envelopeIDPattern is core's opaque-ID shape for authority, principal and
// binding IDs, restated because an admitted unit that core would refuse to read
// in a document can never be matched, and an entry that can never match reads
// as one that works.
var envelopeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}[A-Za-z0-9]$`)

// envelopeDigestPattern is the OCI manifest digest shape. A readable stand-in
// for a digest would approve a build that cannot exist.
var envelopeDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// envelopeVocabularyPattern is the module contract's name shape for a resource
// kind, and its action shape, which a scope ceiling is spelled in.
var (
	envelopeKindPattern   = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	envelopeActionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// authorityOperations are the operations a unit may name, in core's vocabulary:
// the delivered unit ID ends in one of them.
var authorityOperations = []string{
	modulecontract.OperationInvoke, modulecontract.OperationLookup, modulecontract.OperationHeadless,
}

// authorityUnit is an admitted unit ID read back into its three parts.
//
// The ID is opaque to core, which compares it and never parses it. It is NOT
// opaque here: this host is the one the ID is minted for — delivery names each
// unit `<presence binding>:<binding>:<operation>` so the envelope can list it —
// and the host reading its own convention is what lets it refuse a unit filed
// under another presence binding, another operation audience or an operation
// that does not exist.
type authorityUnit struct {
	presenceBinding string
	binding         string
	operation       string
}

func parseAuthorityUnitID(id string) (authorityUnit, bool) {
	last := strings.LastIndex(id, ":")
	if last <= 0 {
		return authorityUnit{}, false
	}
	operation := id[last+1:]
	rest := id[:last]
	middle := strings.LastIndex(rest, ":")
	if middle <= 0 {
		return authorityUnit{}, false
	}
	unit := authorityUnit{presenceBinding: rest[:middle], binding: rest[middle+1:], operation: operation}
	if unit.binding == "" || !slices.Contains(authorityOperations, unit.operation) {
		return authorityUnit{}, false
	}
	return unit, true
}

// ParseModuleEnvelope decodes and validates one envelope document.
//
// Strict at every level: an unknown key is an error, so a field this host does
// not model — a `workload` above all — is refused rather than ignored. Every
// principal's grant is held to the rules the boot-time registry parser applied
// (validateModulePrincipalCeiling), so moving the ceiling from configuration to
// a runtime write relaxed none of them.
func ParseModuleEnvelope(raw []byte) (*ModuleEnvelope, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: the document is empty", ErrModuleEnvelopeInvalid)
	}
	if len(raw) > maxModuleEnvelopeBytes {
		return nil, fmt.Errorf("%w: the document is %d bytes, above the %d-byte bound", ErrModuleEnvelopeInvalid, len(raw), maxModuleEnvelopeBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire moduleEnvelopeWire
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrModuleEnvelopeInvalid, err)
	}
	if decoder.More() {
		return nil, fmt.Errorf("%w: trailing data after the document", ErrModuleEnvelopeInvalid)
	}
	if wire.Revision == 0 {
		return nil, fmt.Errorf("%w: revision starts at 1; 0 names no ceiling", ErrModuleEnvelopeInvalid)
	}
	envelope := &ModuleEnvelope{
		Revision:   wire.Revision,
		Principals: make(map[string]ModuleEnvelopePrincipal, len(wire.Principals)),
	}
	for index, build := range wire.ApprovedBuilds {
		if !envelopeDigestPattern.MatchString(string(build)) {
			return nil, fmt.Errorf("%w: approved build %d is %q, which is not a SHA-256 OCI image manifest digest",
				ErrModuleEnvelopeInvalid, index, build)
		}
		if slices.Contains(envelope.ApprovedBuilds, build) {
			return nil, fmt.Errorf("%w: approved build %q is listed twice", ErrModuleEnvelopeInvalid, build)
		}
		envelope.ApprovedBuilds = append(envelope.ApprovedBuilds, build)
	}
	for prefix, principal := range wire.Principals {
		parsed, err := parseModuleEnvelopePrincipal(prefix, principal)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrModuleEnvelopeInvalid, err)
		}
		envelope.Principals[prefix] = parsed
	}
	return envelope, nil
}

func parseModuleEnvelopePrincipal(prefix string, wire moduleEnvelopePrincipalWire) (ModuleEnvelopePrincipal, error) {
	if err := validateModulePrincipalPrefix(prefix); err != nil {
		return ModuleEnvelopePrincipal{}, err
	}
	grant := wire.Grant.grant(prefix)
	tenant, err := canonicalModuleTenant(prefix, grant.Tenant)
	if err != nil {
		return ModuleEnvelopePrincipal{}, err
	}
	grant.Tenant = tenant
	if err := validateModulePrincipalCeiling(prefix, grant); err != nil {
		return ModuleEnvelopePrincipal{}, err
	}
	// A principal admitting no unit can never be inside the envelope — core
	// refuses a document principal with no binding — so an entry with none is
	// one that reads as allowing a module and allows nothing.
	if len(wire.Bindings) == 0 {
		return ModuleEnvelopePrincipal{}, fmt.Errorf("module principal %q admits no authority unit, so no delivered document can ever be inside its ceiling", prefix)
	}
	seen := make(map[string]bool, len(wire.Bindings))
	for _, id := range wire.Bindings {
		if !envelopeIDPattern.MatchString(id) {
			return ModuleEnvelopePrincipal{}, fmt.Errorf("module principal %q admits unit %q, which is not a valid authority binding ID", prefix, id)
		}
		if seen[id] {
			return ModuleEnvelopePrincipal{}, fmt.Errorf("module principal %q admits unit %q twice", prefix, id)
		}
		seen[id] = true
		unit, ok := parseAuthorityUnitID(id)
		if !ok {
			return ModuleEnvelopePrincipal{}, fmt.Errorf(
				"module principal %q admits unit %q, which is not <presence binding>:<binding>:<operation> with an operation of %s",
				prefix, id, strings.Join(authorityOperations, ", "))
		}
		// Every admitted unit names an operation audience the grant holds, and
		// an operation that audience can grant. A unit with nothing to bound it
		// would be admitted and then grant nothing, which reads as a unit that
		// works.
		audience, declared := grant.OperationAudiences[unit.binding]
		if !declared {
			return ModuleEnvelopePrincipal{}, fmt.Errorf(
				"module principal %q admits unit %q for binding %q, which its grant declares no operation audience for",
				prefix, id, unit.binding)
		}
		if unit.operation == modulecontract.OperationHeadless && audience.HeadlessScopes == nil {
			return ModuleEnvelopePrincipal{}, fmt.Errorf(
				"module principal %q admits headless unit %q, and binding %q grants no headless scopes",
				prefix, id, unit.binding)
		}
	}
	if err := validateEnvelopeScopeCeilings(prefix, wire.ScopeCeilings); err != nil {
		return ModuleEnvelopePrincipal{}, err
	}
	return ModuleEnvelopePrincipal{
		Bindings:      slices.Clone(wire.Bindings),
		ScopeCeilings: slices.Clone(wire.ScopeCeilings),
		Grant:         grant,
	}, nil
}

func validateEnvelopeScopeCeilings(prefix string, ceilings []solutionhost.ScopeCeiling) error {
	kinds := make(map[string]bool, len(ceilings))
	for _, ceiling := range ceilings {
		if !envelopeKindPattern.MatchString(ceiling.ResourceKind) {
			return fmt.Errorf("module principal %q declares a scope ceiling on %q, which is not a resource kind", prefix, ceiling.ResourceKind)
		}
		if kinds[ceiling.ResourceKind] {
			return fmt.Errorf("module principal %q declares two scope ceilings on %q; a ceiling is held whole, so one kind has one", prefix, ceiling.ResourceKind)
		}
		kinds[ceiling.ResourceKind] = true
		if len(ceiling.Actions) == 0 {
			return fmt.Errorf("module principal %q declares a scope ceiling on %q with no action, which bounds nothing while reading as a bound", prefix, ceiling.ResourceKind)
		}
		actions := make(map[string]bool, len(ceiling.Actions))
		for _, action := range ceiling.Actions {
			if !envelopeActionPattern.MatchString(action) || actions[action] {
				return fmt.Errorf("module principal %q declares scope ceiling %q action %q, which is malformed or repeated", prefix, ceiling.ResourceKind, action)
			}
			actions[action] = true
		}
	}
	return nil
}

// admits reports whether this principal's ceiling admits one unit a document
// over presenceBinding grants it.
//
// Exact, and on every dimension the envelope speaks to: the unit ID is listed;
// it is filed under the document's own presence binding, so a unit admitted for
// one instance of a module cannot be claimed by another; its audience is the
// audience the grant's operation audience for that binding names; and a queue
// or namespace it carries is one the grant allows.
func (p ModuleEnvelopePrincipal) admits(presenceBinding string, unit solutionhost.AuthorityBinding) bool {
	if !slices.Contains(p.Bindings, unit.ID) {
		return false
	}
	parsed, ok := parseAuthorityUnitID(unit.ID)
	if !ok || parsed.presenceBinding != presenceBinding {
		return false
	}
	audience, declared := p.Grant.OperationAudiences[parsed.binding]
	if !declared || audience.Audience != unit.Audience {
		return false
	}
	if unit.Queue != "" && !slices.Contains(p.Grant.Queues, unit.Queue) {
		return false
	}
	if unit.Namespace != "" && !slices.Contains(p.Grant.Namespaces, unit.Namespace) {
		return false
	}
	return true
}

// authorityEnvelopeFor projects this ceiling into core's Envelope for ONE
// authority document, which core's ValidateAgainst then holds the document to.
//
// Per document, and that is what keeps containment exact. Core's Envelope lists
// whole (principal, unit) pairs; this record admits unit IDs and bounds each
// unit's audience, queue and namespace through the principal's grant. So the
// projection is the document's own units that this ceiling admits for the
// principal holding them — any unit it does not admit is absent, and core then
// refuses the whole document as claiming more than its envelope allows. The
// queues, namespaces and scope ceilings are those of the principals the
// document grants to, never the union of the host's: a document for one module
// cannot declare another module's queue and pass because someone else may own
// it.
func (e *ModuleEnvelope) authorityEnvelopeFor(document *solutionhost.AuthorityDocument) solutionhost.Envelope {
	if e == nil {
		return solutionhost.Envelope{}
	}
	envelope := solutionhost.Envelope{
		Revision:       e.Revision,
		ApprovedBuilds: slices.Clone(e.ApprovedBuilds),
		Queues:         []string{},
		Namespaces:     []string{},
		ScopeCeilings:  []solutionhost.ScopeCeiling{},
	}
	if document == nil {
		return envelope
	}
	for _, principal := range document.Principals {
		entry, declared := e.Principals[principal.Principal]
		if !declared {
			continue
		}
		envelope.Queues = append(envelope.Queues, entry.Grant.Queues...)
		envelope.Namespaces = append(envelope.Namespaces, entry.Grant.Namespaces...)
		envelope.ScopeCeilings = append(envelope.ScopeCeilings, entry.ScopeCeilings...)
		for _, unit := range principal.Bindings {
			if entry.admits(document.PresenceBinding, unit) {
				envelope.Grants = append(envelope.Grants, solutionhost.EnvelopeGrant{Principal: principal.Principal, Binding: unit})
			}
		}
	}
	return envelope
}

// SetModuleEnvelopeStore wires the envelope's persistence. Boot wiring.
func (s *Service) SetModuleEnvelopeStore(store ModuleEnvelopeStore) {
	s.moduleEnvelopes = store
}

// SetModuleRegistryPassTrigger wires what a delivery or an envelope write calls
// to have the registry reconciled now rather than at the next tick. Boot wiring;
// nil means the next tick is the only pass.
func (s *Service) SetModuleRegistryPassTrigger(trigger func()) {
	s.registryPassTrigger = trigger
}

// requestRegistryPass asks for a reconcile pass without waiting for it.
func (s *Service) requestRegistryPass() {
	if s.registryPassTrigger != nil {
		s.registryPassTrigger()
	}
}

// CurrentModuleEnvelope reads and re-validates the highest revision.
//
// Re-validated on every read rather than trusted as stored: the rules a ceiling
// is held to live in this binary, and a revision written by an older one that a
// newer one refuses must stop granting rather than keep granting because it was
// accepted once. A stored revision that no longer parses is an ERROR, never "no
// envelope" — a reconciler reading it keeps what it last installed.
func (s *Service) CurrentModuleEnvelope(ctx context.Context) (*ModuleEnvelope, error) {
	if s.moduleEnvelopes == nil {
		return nil, ErrModuleEnvelopeUnavailable
	}
	var record *ModuleEnvelopeRecord
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		record, err = s.moduleEnvelopes.LatestModuleEnvelopeRecord(ctx)
		return err
	}); err != nil {
		return nil, fmt.Errorf("read the module envelope: %w", err)
	}
	if record == nil {
		return nil, nil
	}
	envelope, err := ParseModuleEnvelope(record.Document)
	if err != nil {
		return nil, fmt.Errorf("stored module envelope revision %d no longer validates: %w", record.Revision, err)
	}
	if envelope.Revision != record.Revision {
		return nil, fmt.Errorf("stored module envelope revision %d carries document revision %d", record.Revision, envelope.Revision)
	}
	return envelope, nil
}

// GetModuleEnvelope returns the current revision as written, for a platform
// super_admin. Nil means no envelope has been written.
func (s *Service) GetModuleEnvelope(ctx context.Context, actorID string) (*ModuleEnvelopeRecord, error) {
	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, NewStoreError(err, ErrTypePermission)
	}
	if s.moduleEnvelopes == nil {
		return nil, ErrModuleEnvelopeUnavailable
	}
	var record *ModuleEnvelopeRecord
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		record, err = s.moduleEnvelopes.LatestModuleEnvelopeRecord(ctx)
		return err
	}); err != nil {
		return nil, wool.Get(ctx).In("GetModuleEnvelope").Wrapf(err, "cannot read the module envelope")
	}
	return record, nil
}

// PutModuleEnvelope writes the next revision of the ceiling, as a platform
// super_admin, never through an impersonated session (requirePlatformRole).
//
// The document must carry revision expectedRevision+1: the caller states which
// ceiling it replaces and which revision it writes, and a write racing another
// administrator's is refused rather than silently layered on a ceiling the
// caller never read. The row and its audit event commit together.
//
// It takes effect at the next check. A pass is requested on commit, so the
// registry reflects the new ceiling without waiting for the tick — narrowing
// included: an entry the new ceiling no longer allows is removed from the
// registry the next pass installs.
func (s *Service) PutModuleEnvelope(
	ctx context.Context, actorID string, expectedRevision uint64, document []byte,
) (*ModuleEnvelopeRecord, error) {
	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, NewStoreError(err, ErrTypePermission)
	}
	if s.moduleEnvelopes == nil {
		return nil, ErrModuleEnvelopeUnavailable
	}
	envelope, err := ParseModuleEnvelope(document)
	if err != nil {
		return nil, NewStoreError(err, ErrTypeValidation)
	}
	if envelope.Revision != expectedRevision+1 {
		return nil, NewStoreError(fmt.Errorf(
			"%w: the document carries revision %d and replaces revision %d, so it must carry %d",
			ErrModuleEnvelopeInvalid, envelope.Revision, expectedRevision, expectedRevision+1), ErrTypeValidation)
	}
	record := &ModuleEnvelopeRecord{Revision: envelope.Revision, Document: bytes.Clone(document), WrittenBy: actorID}
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		if err := s.moduleEnvelopes.AppendModuleEnvelopeRecord(ctx, record, expectedRevision); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventModuleEnvelopeWritten, "module_envelope",
			strconv.FormatUint(record.Revision, 10), "", map[string]any{
				"revision":          int64(record.Revision),  //nolint:gosec // a revision is a small positive counter
				"previous_revision": int64(expectedRevision), //nolint:gosec // a revision is a small positive counter
				"principals":        len(envelope.Principals),
			})
	}); err != nil {
		if errors.Is(err, ErrModuleEnvelopeConflict) {
			return nil, NewStoreError(err, ErrTypeConflict)
		}
		return nil, wool.Get(ctx).In("PutModuleEnvelope").Wrapf(err, "cannot write the module envelope")
	}
	s.requestRegistryPass()
	return record, nil
}
