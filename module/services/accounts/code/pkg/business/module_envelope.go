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
	"sync"
	"time"

	modulecontract "github.com/codefly-dev/core/contracts/module"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
//   - bindings: the WHOLE authority units this host admits for the principal —
//     id (as delivery names it, <presence binding>:<binding>:<operation>),
//     revision, scope, queue, namespace, binding key and lookup method; the
//     audience is the grant's for that binding. Core's containment is exact
//     element equality against these units, so a document whose unit differs
//     in any field — a wider scope, another revision, another key — is OUTSIDE
//     the envelope and grants nothing at all.
//   - workload_identities: the workloads the principal may run as (SPIFFE ID
//     and authenticating container). The workload is read from delivered
//     presence, and it binds only when it is one of these, so a later presence
//     generation naming another service account or container does not move
//     the principal without a new envelope revision.
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
	// Units are the whole authority units this host admits for the principal,
	// built from the envelope alone: the audience is the grant's operation
	// audience for the unit's binding. They are what core's exact containment
	// compares a delivered unit against, so no field of them may come from the
	// document being judged.
	Units []solutionhost.AuthorityBinding
	// WorkloadIdentities are the workloads the principal may run as.
	WorkloadIdentities []ModuleWorkloadIdentity
	// ScopeCeilings are the vocabulary entries the subject module may declare.
	ScopeCeilings []solutionhost.ScopeCeiling
	// Grant is the registry entry's ceiling. Prefix is the key it is filed
	// under and Workload is always empty: the workload is derived.
	Grant ModulePrincipalGrant
}

// ModuleWorkloadIdentity is one workload a principal may run as: the SPIFFE ID
// the presence document names for it and its authenticating container.
type ModuleWorkloadIdentity struct {
	SPIFFEID  string `json:"spiffe_id"`
	Container string `json:"container"`
}

// workload is the identity as the registry holds it: namespace and service
// account read from the SPIFFE ID by the one strict parser. A SPIFFE ID it
// refuses yields an empty workload, which validation refuses in turn.
func (w ModuleWorkloadIdentity) workload() ModuleWorkload {
	identity, err := ParseKubernetesSPIFFEID(w.SPIFFEID)
	if err != nil {
		return ModuleWorkload{Container: w.Container}
	}
	return ModuleWorkload{SPIFFEID: w.SPIFFEID, ServiceAccount: identity.ServiceAccount, Namespace: identity.Namespace, Container: w.Container}
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
	// ModuleInstallations enumerates active registry-associated consent in the
	// envelope writer's transaction, after it takes the installation fence.
	ModuleInstallations(context.Context) ([]ModuleInstallationConsent, error)
	// LatestModuleEnvelopeRecord returns the highest revision, or nil when no
	// envelope has ever been written.
	LatestModuleEnvelopeRecord(ctx context.Context) (*ModuleEnvelopeRecord, error)
	// AppendModuleEnvelopeRecord writes record.Revision only while the current
	// revision is expectedRevision (0 = none written), and returns
	// ErrModuleEnvelopeConflict otherwise. The store fills WrittenAt.
	AppendModuleEnvelopeRecord(ctx context.Context, record *ModuleEnvelopeRecord, expectedRevision uint64) error
	// LatestModuleEnvelopeRevision is the highest revision written, 0 when
	// none. It is the fence every capability check reads: a registry derived
	// from an older revision than this serves nothing.
	LatestModuleEnvelopeRevision(ctx context.Context) (uint64, error)
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
	Bindings           []moduleEnvelopeUnitWire    `json:"bindings"`
	WorkloadIdentities []ModuleWorkloadIdentity    `json:"workload_identities"`
	ScopeCeilings      []solutionhost.ScopeCeiling `json:"scope_ceilings"`
	Grant              moduleEnvelopeGrantWire     `json:"grant"`
}

// moduleEnvelopeUnitWire is one admitted unit as an administrator writes it.
// The audience is not here: it is the grant's operation audience for the
// unit's binding, so the two can never disagree.
type moduleEnvelopeUnitWire struct {
	ID           string `json:"id"`
	Revision     uint64 `json:"revision"`
	Scope        string `json:"scope"`
	Queue        string `json:"queue,omitempty"`
	Namespace    string `json:"namespace,omitempty"`
	BindingKey   string `json:"binding_key,omitempty"`
	LookupMethod string `json:"lookup_method,omitempty"`
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

// workloadOwners refuses two principals allowing one Kubernetes workload
// identity (namespace and service account).
func (e *ModuleEnvelope) workloadOwners() error {
	owner := map[ModuleWorkload]string{}
	prefixes := make([]string, 0, len(e.Principals))
	for prefix := range e.Principals {
		prefixes = append(prefixes, prefix)
	}
	slices.Sort(prefixes)
	for _, prefix := range prefixes {
		for _, identity := range e.Principals[prefix].WorkloadIdentities {
			workload := identity.workload()
			key := ModuleWorkload{Namespace: workload.Namespace, ServiceAccount: workload.ServiceAccount}
			if other, taken := owner[key]; taken && other != prefix {
				return fmt.Errorf("module principals %q and %q both allow workload %s/%s; one workload identity maps to exactly one principal",
					other, prefix, key.Namespace, key.ServiceAccount)
			}
			owner[key] = prefix
		}
	}
	return nil
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
	// ONE WORKLOAD, ONE PRINCIPAL. A pod's token names its namespace and
	// service account — never its container — so two principals allowing the
	// same (namespace, service account) would let one module's pod token
	// authorize the other module's prefix. Refused by name, whatever the
	// containers.
	if err := envelope.workloadOwners(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrModuleEnvelopeInvalid, err)
	}
	return envelope, nil
}

func parseModuleEnvelopePrincipal(prefix string, wire moduleEnvelopePrincipalWire) (ModuleEnvelopePrincipal, error) {
	if err := validateModulePrincipalPrefix(prefix); err != nil {
		return ModuleEnvelopePrincipal{}, err
	}
	grant := wire.Grant.grant(prefix)
	// The install-step queue is the host's own: its jobs are other solutions'
	// install steps, and a module granted it could claim and complete one.
	if slices.Contains(grant.Queues, InstallationStepQueue) {
		return ModuleEnvelopePrincipal{}, fmt.Errorf("principal %q is granted the queue %q, which is the host's own and is granted to no module", prefix, InstallationStepQueue)
	}
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
	units := make([]solutionhost.AuthorityBinding, 0, len(wire.Bindings))
	for _, declared := range wire.Bindings {
		unit, err := parseEnvelopeUnit(prefix, grant, declared)
		if err != nil {
			return ModuleEnvelopePrincipal{}, err
		}
		if seen[unit.ID] {
			return ModuleEnvelopePrincipal{}, fmt.Errorf("module principal %q admits unit %q twice", prefix, unit.ID)
		}
		seen[unit.ID] = true
		units = append(units, unit)
	}
	// The workloads the principal may run as. Required: a principal whose
	// workload no administrator approved would take whatever service account
	// the next presence generation names.
	if len(wire.WorkloadIdentities) == 0 {
		return ModuleEnvelopePrincipal{}, fmt.Errorf("module principal %q allows no workload identity, so no delivered presence can ever bind it", prefix)
	}
	for _, identity := range wire.WorkloadIdentities {
		if _, err := ParseKubernetesSPIFFEID(identity.SPIFFEID); err != nil {
			return ModuleEnvelopePrincipal{}, fmt.Errorf("module principal %q allows workload identity: %w", prefix, err)
		}
		if err := identity.workload().validateForm(prefix); err != nil {
			return ModuleEnvelopePrincipal{}, fmt.Errorf("module principal %q allows workload identity %q in container %q: %w",
				prefix, identity.SPIFFEID, identity.Container, err)
		}
	}
	if err := validateEnvelopeScopeCeilings(prefix, wire.ScopeCeilings); err != nil {
		return ModuleEnvelopePrincipal{}, err
	}
	return ModuleEnvelopePrincipal{
		Units:              units,
		WorkloadIdentities: slices.Clone(wire.WorkloadIdentities),
		ScopeCeilings:      slices.Clone(wire.ScopeCeilings),
		Grant:              grant,
	}, nil
}

// parseEnvelopeUnit builds one whole admitted unit from the envelope alone and
// holds it to the grant it sits under: its binding names an operation audience
// the grant declares, its scope is canonical and within that operation's
// scopes, and its queue and namespace are ones the grant allows. A unit the
// grant could never honour is refused here, where the administrator reads the
// reason, instead of being admitted and then clipped away.
func parseEnvelopeUnit(prefix string, grant ModulePrincipalGrant, declared moduleEnvelopeUnitWire) (solutionhost.AuthorityBinding, error) {
	refuse := func(format string, args ...any) (solutionhost.AuthorityBinding, error) {
		return solutionhost.AuthorityBinding{}, fmt.Errorf("module principal %q admits unit %q: "+format,
			append([]any{prefix, declared.ID}, args...)...)
	}
	if !envelopeIDPattern.MatchString(declared.ID) {
		return refuse("not a valid authority binding ID")
	}
	unit, ok := parseAuthorityUnitID(declared.ID)
	if !ok {
		return refuse("not <presence binding>:<binding>:<operation> with an operation of %s", strings.Join(authorityOperations, ", "))
	}
	if declared.Revision == 0 {
		return refuse("revision starts at 1")
	}
	audience, defined := grant.OperationAudiences[unit.binding]
	if !defined {
		return refuse("binding %q has no operation audience in the grant", unit.binding)
	}
	var allowed []ModuleOperationScope
	switch unit.operation {
	case modulecontract.OperationInvoke:
		allowed = audience.InvokeScopes
	case modulecontract.OperationLookup:
		allowed = audience.LookupScopes
	case modulecontract.OperationHeadless:
		if audience.HeadlessScopes == nil {
			return refuse("binding %q grants no headless scopes", unit.binding)
		}
		allowed = audience.HeadlessScopes
	}
	values := strings.Split(declared.Scope, ",")
	if declared.Scope == "" || !slices.IsSorted(values) {
		return refuse("scope %q is not the sorted, comma-joined kind:action values delivery writes", declared.Scope)
	}
	for index, value := range values {
		kind, action, found := strings.Cut(value, ":")
		if !found || !envelopeKindPattern.MatchString(kind) || !envelopeActionPattern.MatchString(action) || index > 0 && values[index-1] == value {
			return refuse("scope value %q is not one kind:action", value)
		}
		if !slices.ContainsFunc(allowed, func(scope ModuleOperationScope) bool {
			return scope.ResourceKind == kind && slices.Contains(scope.Actions, action)
		}) {
			return refuse("scope %q is wider than binding %q allows for %s", value, unit.binding, unit.operation)
		}
	}
	if declared.Queue != "" && !slices.Contains(grant.Queues, declared.Queue) {
		return refuse("queue %q is not one the grant allows", declared.Queue)
	}
	if declared.Namespace != "" && !slices.Contains(grant.Namespaces, declared.Namespace) {
		return refuse("namespace %q is not one the grant allows", declared.Namespace)
	}
	for _, field := range []string{declared.BindingKey, declared.LookupMethod} {
		if field != "" && !validOperationValue(field, 256) {
			return refuse("binding key or lookup method %q is not a single-line value", field)
		}
	}
	return solutionhost.AuthorityBinding{
		ID: declared.ID, Revision: declared.Revision, Audience: audience.Audience, Scope: declared.Scope,
		Queue: declared.Queue, Namespace: declared.Namespace,
		BindingKey: declared.BindingKey, LookupMethod: declared.LookupMethod,
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

// unitsOver are the units this principal's ceiling admits over one presence
// binding: the units filed under it, so a unit admitted for one instance of a
// module is never offered to a document over another.
func (p ModuleEnvelopePrincipal) unitsOver(presenceBinding string) []solutionhost.AuthorityBinding {
	var over []solutionhost.AuthorityBinding
	for _, unit := range p.Units {
		if parsed, ok := parseAuthorityUnitID(unit.ID); ok && parsed.presenceBinding == presenceBinding {
			over = append(over, unit)
		}
	}
	return over
}

// authorityEnvelopeFor projects this ceiling into core's Envelope for ONE
// authority document, which core's ValidateAgainst then holds the document to.
//
// EVERY FIELD OF EVERY GRANT COMES FROM THE ENVELOPE. Core's containment is
// exact element equality between the document's (principal, unit) pairs and
// the envelope's, so the envelope side must be the host's own: the units the
// administrator admitted, whole — id, revision, audience, scope, queue,
// namespace, binding key, lookup method. A document whose unit differs in any
// of those is outside and grants nothing. The first version appended the
// DOCUMENT'S units to this list once their id was admitted, which made the
// ceiling partly the document's own — a wider scope or another revision
// compared equal to itself.
//
// Per document, too: the units are those filed under the document's presence
// binding, for the principals the document grants to, and the queues,
// namespaces and scope ceilings are those principals', never the host's union.
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
		for _, unit := range entry.unitsOver(document.PresenceBinding) {
			envelope.Grants = append(envelope.Grants, solutionhost.EnvelopeGrant{Principal: principal.Principal, Binding: unit})
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
	// Observed BEFORE it is parsed: a revision that is committed and cannot be
	// read still fences the registry derived from the one before it.
	s.observeModuleEnvelopeRevision(record.Revision)
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
	var policy map[string]any
	if err := json.Unmarshal(document, &policy); err != nil {
		return nil, err
	}
	entry := &PolicyLogEntry{OperationID: newPolicyLogOperationID("module-envelope"),
		Decision: PolicyLogNarrowed, SubjectKind: PolicyLogModuleEnvelope, SubjectID: "module-envelope",
		Policy: policy, EnvelopeRevision: envelope.Revision, Actor: actorID}
	// witnessed is set before apply runs under the policy log; apply refuses
	// to revoke anything while it is false.
	witnessed := false
	apply := func(ctx context.Context) error {
		if err := s.moduleEnvelopes.AppendModuleEnvelopeRecord(ctx, record, expectedRevision); err != nil {
			return err
		}
		installations, err := s.moduleEnvelopes.ModuleInstallations(ctx)
		if err != nil {
			return err
		}
		for _, installation := range installations {
			if installationTenancyAllowed(envelope, installation.PrincipalID, installation.OrgID) {
				continue
			}
			// An installation acknowledged between the classification and
			// this transaction is found here and nowhere else. Revoking it is
			// a narrowing, so the write that was classified a pure grant
			// stops and is taken again under the witness.
			if !witnessed {
				return errModuleEnvelopeNarrowsAfterAll
			}
			if err := s.uninstallSolutionTx(ctx, actorID, "user", installation.OrgID, installation.ID); err != nil {
				return err
			}
		}
		return s.emitTx(ctx, actorID, "user", EventModuleEnvelopeWritten, "module_envelope",
			strconv.FormatUint(record.Revision, 10), "", map[string]any{
				"revision":          int64(record.Revision),  //nolint:gosec // a revision is a small positive counter
				"previous_revision": int64(expectedRevision), //nolint:gosec // a revision is a small positive counter
				"principals":        len(envelope.Principals),
			})
	}
	// The policy log witnesses NARROWINGS — "a narrowing nothing witnessed is
	// one a restore undoes silently". A write that only grants (a first
	// envelope, a build or a principal added, nothing an installation loses)
	// is not one: a restore that undoes it leaves less authority, which is the
	// fail-closed direction, so it is applied and audited without a witness.
	// Anything that removes or changes what was granted is witnessed as today.
	narrows, err := s.moduleEnvelopeNarrows(ctx, envelope, document)
	if err != nil {
		return nil, err
	}
	underWitness := func(ctx context.Context) error {
		witnessed = true
		return s.WithPolicyLoggedNarrowing(ctx, entry, apply)
	}
	write := underWitness
	if !narrows {
		write = func(ctx context.Context) error {
			err := s.store.WithControlPlane(ctx, apply)
			if errors.Is(err, errModuleEnvelopeNarrowsAfterAll) {
				return underWitness(ctx)
			}
			return err
		}
	}
	if err := write(ctx); err != nil {
		if errors.Is(err, ErrModuleEnvelopeConflict) {
			return nil, NewStoreError(err, ErrTypeConflict)
		}
		return nil, wool.Get(ctx).In("PutModuleEnvelope").Wrapf(err, "cannot write the module envelope")
	}
	// This replica fences its registry at once — it serves nothing derived
	// from the replaced ceiling — and asks for the pass that derives the new
	// one. Every other replica fences at its next authority decision, which
	// reads the committed revision (fenceModuleRegistrySnapshot).
	s.observeModuleEnvelopeRevision(record.Revision)
	s.requestRegistryPass()
	return record, nil
}

// ErrModuleRegistryStale reports a registry derived from an envelope revision
// older than the committed one. The registry may hold what the newer ceiling
// withdrew, so nothing is authorized from it until a pass derives the current
// one; the refusal is Unavailable because the caller's retry succeeds once it
// has.
var ErrModuleRegistryStale = errors.New("the module registry predates the committed module envelope")

// errModuleEnvelopeNarrowsAfterAll stops an envelope write that was classified
// a pure grant and then found something to revoke. It never leaves this file:
// the write is retried under the witness.
var errModuleEnvelopeNarrowsAfterAll = errors.New("the envelope write revokes an installation and was not witnessed")

// moduleRegistrySnapshot is the installed registry and the envelope revision
// it was derived from, loaded ONCE. A decision reads its grant from this value
// and fences this value — never a second load — so a registry installed between
// the two can neither lend its revision to a grant it does not hold nor hand a
// grant to a fence it did not pass.
func (s *Service) moduleRegistrySnapshot() *installedModuleRegistry {
	if installed := s.modulePrincipals.Load(); installed != nil {
		return installed
	}
	return &installedModuleRegistry{}
}

// fenceModuleRegistrySnapshot is the revision fence every module authority
// decision passes: it reads the committed envelope revision from the store and
// refuses unless THIS snapshot was derived from exactly that revision.
//
// Durable and per replica. The registry is in memory and each replica derives
// its own, on its own pass; the committed revision is in the store. Comparing
// the snapshot a decision authorizes from with the store is what makes a
// narrowing written on one replica — or committed while a pass was reading the
// previous ceiling, or installed while this decision was in flight — refuse at
// the next decision on every replica, rather than at each one's next pass.
//
// Where no envelope store is wired (a registry stated directly by a test)
// there is no committed revision to compare with.
func (s *Service) fenceModuleRegistrySnapshot(ctx context.Context, snapshot *installedModuleRegistry) error {
	if s.moduleEnvelopes == nil {
		return nil
	}
	latest, err := s.committedModuleEnvelopeRevision(ctx)
	if err != nil {
		return status.Errorf(codes.Unavailable, "cannot read the committed module envelope revision: %v", err)
	}
	s.observeModuleEnvelopeRevision(latest)
	if latest != snapshot.envelopeRevision {
		s.requestRegistryPass()
		return wrapStatus(codes.Unavailable, ErrModuleRegistryStale,
			"the registry was derived from envelope revision %d and revision %d is committed", snapshot.envelopeRevision, latest)
	}
	return nil
}

// moduleEnvelopeRevisionMemo carries one committed-revision read for the
// lifetime of a request context, so a request that makes several module
// authority decisions pays for one read. The value is the revision committed
// when the request first asked, which is the instant its decisions are judged
// at.
type moduleEnvelopeRevisionMemo struct {
	once     sync.Once
	revision uint64
	err      error
}

type moduleEnvelopeRevisionMemoKey struct{}

// WithModuleEnvelopeRevisionMemo scopes one committed-revision read to ctx and
// everything derived from it.
func WithModuleEnvelopeRevisionMemo(ctx context.Context) context.Context {
	if _, already := ctx.Value(moduleEnvelopeRevisionMemoKey{}).(*moduleEnvelopeRevisionMemo); already {
		return ctx
	}
	return context.WithValue(ctx, moduleEnvelopeRevisionMemoKey{}, &moduleEnvelopeRevisionMemo{})
}

func (s *Service) committedModuleEnvelopeRevision(ctx context.Context) (uint64, error) {
	read := func() (uint64, error) {
		var latest uint64
		err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			var err error
			latest, err = s.moduleEnvelopes.LatestModuleEnvelopeRevision(ctx)
			return err
		})
		return latest, err
	}
	memo, scoped := ctx.Value(moduleEnvelopeRevisionMemoKey{}).(*moduleEnvelopeRevisionMemo)
	if !scoped {
		return read()
	}
	memo.once.Do(func() { memo.revision, memo.err = read() })
	return memo.revision, memo.err
}

// currentModuleGrant resolves a principal's grant from ONE registry snapshot
// and fences that snapshot. found is false only for a principal the snapshot
// does not hold and that is not fenced; a fenced or stale registry is an
// error, so a caller never mistakes "not current" for "not a module".
func (s *Service) currentModuleGrant(ctx context.Context, principalID string) (ModulePrincipalGrant, bool, error) {
	grant, found, _, err := s.currentModuleGrantIn(ctx, principalID)
	return grant, found, err
}

// currentModuleGrantIn is currentModuleGrant that also returns the fenced
// snapshot, so a decision that needs a SECOND principal's grant — a delegator
// beside its caller — reads it from the same registry the fence passed, never
// from a later load.
func (s *Service) currentModuleGrantIn(
	ctx context.Context, principalID string,
) (ModulePrincipalGrant, bool, *installedModuleRegistry, error) {
	snapshot := s.moduleRegistrySnapshot()
	grant, found, err := s.currentModuleGrantFromSnapshot(ctx, snapshot, principalID)
	return grant, found, snapshot, err
}

// currentModuleGrantFromSnapshot fences and resolves an already captured registry.
func (s *Service) currentModuleGrantFromSnapshot(
	ctx context.Context, snapshot *installedModuleRegistry, principalID string,
) (ModulePrincipalGrant, bool, error) {
	grant, found := snapshot.registry[principalID]
	if !found {
		if s.moduleEnvelopes != nil && s.envelopeRevisionSeen.Load() > snapshot.envelopeRevision {
			s.requestRegistryPass()
			return ModulePrincipalGrant{}, false, wrapStatus(codes.Unavailable, ErrModuleRegistryStale,
				"principal %s: a newer module envelope is committed than the registry was derived from", principalID)
		}
		return ModulePrincipalGrant{}, false, nil
	}
	if err := s.fenceModuleRegistrySnapshot(ctx, snapshot); err != nil {
		return ModulePrincipalGrant{}, true, err
	}
	return grant, true, nil
}

// deliveredSince reports that the delivery inbox holds a document the registry
// in snapshot was derived without.
//
// It is the delivery half of the envelope fence above, for the one answer that
// fence does not cover. A principal the registry does not hold is "not a
// declared module principal" — a judgement, which ends the workload that asked.
// That is right when nothing declares it and wrong for the moment between a
// document being accepted and the pass it requested finishing: the host has
// been told, and has not yet read what it was told. A workload that starts
// beside its own delivery asks in exactly that moment, and it must hear "not
// established yet", which it retries, rather than a refusal.
//
// Durable and per replica, like the fence: the inbox is in the store and each
// replica derives its own registry, so the comparison is between the snapshot
// this decision reads and the store, never between two in-memory views.
//
// Nothing is granted here and nothing is widened. The only answer it changes
// is which refusal an undeclared principal hears, and only while a delivered
// document is unread. A document that has been read and grants this principal
// nothing leaves the count equal, and the answer is the judgement it always
// was. Delivery is authenticated and signed, so a caller cannot manufacture
// the condition. Where the registry carries no marker — it was stated
// directly, or published degraded — or the inbox cannot be counted, the answer
// is unchanged.
func (s *Service) deliveredSince(ctx context.Context, snapshot *installedModuleRegistry) bool {
	if snapshot == nil || snapshot.delivered == nil {
		return false
	}
	counter, counts := s.deliveryStore.(SolutionDeliveryCounter)
	if !counts {
		return false
	}
	var delivered, unapplied uint64
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		if delivered, err = counter.CountDeliveredDocuments(ctx); err != nil {
			return err
		}
		unapplied, err = counter.CountUnappliedPresence(ctx)
		return err
	}); err != nil {
		return false
	}
	// A document the pass did not read, or a presence the pass read before the
	// binding reconciler applied it: either way the registry is about to
	// change. The second is the common one — the two reconcilers run apart,
	// and a registry derived between them omits the principal whose presence
	// was applied a moment later.
	return delivered > *snapshot.delivered || unapplied > 0
}

// ModuleInstallationConsent identifies the rows a tenancy change may revoke.
// Agent-only legacy installs have no module principal and are not inferred here.
type ModuleInstallationConsent struct{ ID, OrgID, PrincipalID string }

func installationTenancyAllowed(envelope *ModuleEnvelope, principalID, orgID string) bool {
	for prefix, principal := range envelope.Principals {
		if ModulePrincipalID(prefix) == principalID {
			return moduleAvailableToOrganization(principal.Grant, orgID)
		}
	}
	return false
}

// moduleEnvelopeNarrows reports whether writing next would take authority away
// from anyone, and it is the boundary of the one write that skips the
// policy-log witness, so it recognises a pure grant and calls everything else a
// narrowing.
//
// A pure grant is exactly this, against the envelope in force: every approved
// build is still approved, every principal is still there with a section that
// is the same JSON value, every other top-level key is the same JSON value,
// and no installation loses its organization. What may differ is the
// revision, builds that were added, and whole principals that were added.
//
// A principal's section is compared for equality and never for "at least as
// much". The schema does not let more JSON mean more authority: a resource
// list that appears narrows an audience that was unrestricted, a required
// kind that appears refuses artifacts that passed, a tenant that is set
// removes every other organization, and a unit that gains a field is a
// different unit under core's whole-unit containment. A comparison that tried
// to tell those apart would be a second copy of every rule that reads the
// envelope, wrong whenever one of them moved. So a changed section is a
// narrowing, whatever changed.
//
// A host with no envelope has granted nothing, so its first envelope narrows
// nothing an envelope granted; installations are still checked.
func (s *Service) moduleEnvelopeNarrows(ctx context.Context, next *ModuleEnvelope, nextDocument []byte) (bool, error) {
	narrows := false
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		current, err := s.moduleEnvelopes.LatestModuleEnvelopeRecord(ctx)
		if err != nil {
			return err
		}
		if current != nil && !moduleEnvelopeOnlyAdds(current.Document, nextDocument) {
			narrows = true
			return nil
		}
		// Inside the control plane, like the apply's own read: outside it a
		// row policy hides every installation, and the answer is "none lose
		// anything" for the wrong reason.
		installations, err := s.moduleEnvelopes.ModuleInstallations(ctx)
		if err != nil {
			return err
		}
		for _, installation := range installations {
			if !installationTenancyAllowed(next, installation.PrincipalID, installation.OrgID) {
				narrows = true
				return nil
			}
		}
		return nil
	})
	return narrows, err
}

// moduleEnvelopeOnlyAdds reports that after is before plus approved builds
// and whole principals, and nothing else: see moduleEnvelopeNarrows. Anything
// it cannot read as that — a document that does not decode, a key it does not
// know — is not an addition.
func moduleEnvelopeOnlyAdds(before, after []byte) bool {
	was, ok := envelopeTopLevel(before)
	if !ok {
		return false
	}
	is, ok := envelopeTopLevel(after)
	if !ok {
		return false
	}
	for key := range was {
		if _, kept := is[key]; !kept {
			return false
		}
	}
	for key, value := range is {
		switch key {
		case "revision":
		case "approved_builds":
			if !envelopeBuildsKept(was[key], value) {
				return false
			}
		case "principals":
			if !envelopePrincipalsKept(was[key], value) {
				return false
			}
		default:
			previous, existed := was[key]
			if !existed || !sameJSONValue(previous, value) {
				return false
			}
		}
	}
	return true
}

func envelopeTopLevel(document []byte) (map[string]json.RawMessage, bool) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(document, &top); err != nil || top == nil {
		return nil, false
	}
	return top, true
}

func envelopeBuildsKept(before, after json.RawMessage) bool {
	var was, is []string
	if len(before) > 0 && json.Unmarshal(before, &was) != nil {
		return false
	}
	if len(after) > 0 && json.Unmarshal(after, &is) != nil {
		return false
	}
	for _, build := range was {
		if !slices.Contains(is, build) {
			return false
		}
	}
	return true
}

func envelopePrincipalsKept(before, after json.RawMessage) bool {
	var was, is map[string]json.RawMessage
	if len(before) > 0 && json.Unmarshal(before, &was) != nil {
		return false
	}
	if len(after) > 0 && json.Unmarshal(after, &is) != nil {
		return false
	}
	for prefix, section := range was {
		kept, exists := is[prefix]
		if !exists || !sameJSONValue(section, kept) {
			return false
		}
	}
	return true
}

// sameJSONValue reports that two encodings are one JSON value: key order and
// whitespace aside, every member, element and number literal equal. Numbers
// are compared as written, so two revisions past 2^53 that a float would fold
// together stay different.
func sameJSONValue(a, b json.RawMessage) bool {
	canonical := func(raw json.RawMessage) ([]byte, bool) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, false
		}
		if decoder.More() {
			return nil, false
		}
		encoded, err := json.Marshal(value)
		return encoded, err == nil
	}
	left, ok := canonical(a)
	if !ok {
		return false
	}
	right, ok := canonical(b)
	return ok && bytes.Equal(left, right)
}
