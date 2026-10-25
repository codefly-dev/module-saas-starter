package business

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

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
// WHAT IT HOLDS. Per module principal — keyed by the module's principal, the
// name its manifest's authority request is made under — the GRANT: the ceiling
// of the registry entry the principal can be given (tenant, cross_tenant,
// queues, namespaces, external namespaces, resources, read and operation
// audiences, artifact policies). It carries NO workload and approves NO build:
// which workloads a principal runs as is the applied module manifest's, and
// "only approved images run" is deploy-time admission, outside this host.
//
// WHAT IT IS NOT. It is not the registry, and it is not required. The
// registry is DERIVED each reconcile pass from the applied module manifests
// (module_principal_reconcile.go), and the ceiling is the manifest: a host no
// envelope was written to grants each signed manifest what it asks for, so a
// composition runs with no hand-written authority. Where an envelope IS
// written it narrows: a principal is in the registry only while both say so —
// a signed manifest asks for it AND this envelope allows it — and it holds the
// intersection of the two, never more than either.

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
	// Revision is this ceiling's revision, from 1.
	Revision uint64
	// Principals is the ceiling per module principal, keyed by principal.
	Principals map[string]ModuleEnvelopePrincipal
}

// ModuleEnvelopePrincipal is the ceiling for one module principal.
type ModuleEnvelopePrincipal struct {
	// Grant is the registry entry's ceiling. Prefix is the key it is filed
	// under and Workloads is always empty: the workloads are the manifest's.
	Grant ModulePrincipalGrant
}

// ModuleEnvelopeRecord is one stored revision: the document as written, and who
// wrote it when.
//
// A revision is written by a platform administrator (WrittenBy) or DELIVERED
// from the composition (module_envelope_delivery.go), never both. A delivered
// revision leaves WrittenBy empty and carries the release key id that signed it,
// the ownership domain it speaks for, the canonical payload the signature covers
// and the DSSE envelope verbatim, so it can be re-verified and the next delivery
// admitted against it.
type ModuleEnvelopeRecord struct {
	Revision        uint64
	Document        []byte
	WrittenBy       string
	WrittenAt       time.Time
	Signer          string
	OwnershipDomain string
	Payload         []byte
	Envelope        []byte
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
	// AppendModuleEnvelopeRecord writes record.Revision, which must be above
	// expectedRevision, only while the current revision is expectedRevision
	// (0 = none written), and returns ErrModuleEnvelopeConflict otherwise. The
	// store fills WrittenAt. An administrator writes the next revision; a
	// delivery writes the revision its composition declared, which may skip.
	AppendModuleEnvelopeRecord(ctx context.Context, record *ModuleEnvelopeRecord, expectedRevision uint64) error
	// LatestDeliveredModuleEnvelopeRecord returns the highest DELIVERED
	// revision with its payload and envelope, or nil when none was delivered.
	LatestDeliveredModuleEnvelopeRecord(ctx context.Context) (*ModuleEnvelopeRecord, error)
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
	Revision   uint64                                 `json:"revision"`
	Principals map[string]moduleEnvelopePrincipalWire `json:"principals"`
}

type moduleEnvelopePrincipalWire struct {
	Grant moduleEnvelopeGrantWire `json:"grant"`
}

// moduleEnvelopeGrantWire is a registry entry's ceiling as an administrator
// writes it. It has no `workload` key, and the decoder is strict, so an envelope
// that tries to name the pods a principal runs as is refused by name rather
// than half-applied: the workloads are the applied manifest's.
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
	return ModuleEnvelopePrincipal{Grant: grant}, nil
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
//
// A DELIVERED revision is re-verified against the host's pinned keys too, the
// way the manifest half of the same pass re-verifies every applied manifest.
// Parsing proves the document is well formed, not that anybody may still state
// it: unpinning a compromised release key is the operator's revocation lever,
// and without this the ceiling that key signed would keep granting forever,
// withdrawable only by an administrator's interactive write at a higher
// revision. Its failure is an error for the same reason a parse failure is.
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
	if err := s.reverifyDeliveredModuleEnvelope(ctx, record); err != nil {
		return nil, err
	}
	return envelope, nil
}

// reverifyDeliveredModuleEnvelope re-verifies a delivered revision's signature
// against the host's pinned keys. An interactively written revision carries no
// carrier and is this host's own record, so there is nothing to verify; a
// delivered one is somebody else's statement, held every read to the custody
// that may make it.
//
// A host with no verifier wired cannot answer the question, so it refuses
// rather than skipping: a cell that has received a delivered envelope has a
// verifier by construction, and the shape that has not is a native profile,
// whose envelope is written interactively.
func (s *Service) reverifyDeliveredModuleEnvelope(ctx context.Context, record *ModuleEnvelopeRecord) error {
	if len(record.Payload) == 0 && len(record.Envelope) == 0 {
		return nil
	}
	if s.deliveryVerifier == nil {
		return fmt.Errorf("module envelope revision %d was delivered and this host has no delivery verifier to re-verify it against", record.Revision)
	}
	signed := &solutionhost.Signed{Document: record.Payload, Envelope: record.Envelope}
	if _, err := solutionhost.VerifyDeliveredEnvelope(ctx, signed, s.deliveryVerifier); err != nil {
		return fmt.Errorf("delivered module envelope revision %d no longer verifies against this host's pinned keys: %w", record.Revision, err)
	}
	return nil
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

// ModuleAuthorityRequests is what the applied module manifests ask for, for a
// platform super_admin deciding the next revision of the ceiling. It is the
// reconcile pass's own reading of the applied state (AppliedAuthorityRequests):
// what is asked for, never what is held.
func (s *Service) ModuleAuthorityRequests(ctx context.Context, actorID string) ([]ModuleAuthorityRequest, error) {
	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, NewStoreError(err, ErrTypePermission)
	}
	records, err := s.ListSolutionHostBindings(ctx)
	if err != nil {
		return nil, wool.Get(ctx).In("ModuleAuthorityRequests").Wrapf(err, "cannot read the applied module manifests")
	}
	return AppliedAuthorityRequests(records), nil
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
	err = s.writeModuleEnvelope(ctx, moduleEnvelopeWrite{
		envelope: envelope, record: record, expectedRevision: expectedRevision,
		actorID: actorID, actorType: ActorTypeUser,
		audit: func(ctx context.Context) error {
			return s.emitTx(ctx, actorID, ActorTypeUser, EventModuleEnvelopeWritten, "module_envelope",
				strconv.FormatUint(record.Revision, 10), "", map[string]any{
					"revision":          int64(record.Revision),  //nolint:gosec // a revision is a small positive counter
					"previous_revision": int64(expectedRevision), //nolint:gosec // a revision is a small positive counter
					"principals":        len(envelope.Principals),
				})
		},
	})
	if err != nil {
		if errors.Is(err, ErrModuleEnvelopeConflict) {
			return nil, NewStoreError(err, ErrTypeConflict)
		}
		return nil, wool.Get(ctx).In("PutModuleEnvelope").Wrapf(err, "cannot write the module envelope")
	}
	return record, nil
}

// moduleEnvelopeWrite is one revision on its way to the store and who writes
// it: a platform administrator interactively, or a delivery.
type moduleEnvelopeWrite struct {
	envelope         *ModuleEnvelope
	record           *ModuleEnvelopeRecord
	expectedRevision uint64
	// actorID and actorType attribute the policy-log entry and every
	// installation the write revokes.
	actorID, actorType string
	// audit writes the writer's own event, inside the row's transaction.
	audit func(ctx context.Context) error
}

// writeModuleEnvelope is the ONE writer of the ceiling, whoever wrote the
// revision: it classifies the write, witnesses a narrowing in the policy log,
// appends the row over the expected revision, revokes the installations the
// new ceiling no longer allows, commits the writer's audit event with the row,
// fences this replica and asks for a pass. Its callers authorise the writer
// and validate the document; nothing here relaxes for either of them.
func (s *Service) writeModuleEnvelope(ctx context.Context, w moduleEnvelopeWrite) error {
	var policy map[string]any
	if err := json.Unmarshal(w.record.Document, &policy); err != nil {
		return err
	}
	entry := &PolicyLogEntry{OperationID: newPolicyLogOperationID("module-envelope"),
		Decision: PolicyLogNarrowed, SubjectKind: PolicyLogModuleEnvelope, SubjectID: "module-envelope",
		Policy: policy, EnvelopeRevision: w.envelope.Revision, Actor: w.actorID}
	// witnessed is set before apply runs under the policy log; apply refuses
	// to revoke anything while it is false.
	witnessed := false
	apply := func(ctx context.Context) error {
		if err := s.moduleEnvelopes.AppendModuleEnvelopeRecord(ctx, w.record, w.expectedRevision); err != nil {
			return err
		}
		installations, err := s.moduleEnvelopes.ModuleInstallations(ctx)
		if err != nil {
			return err
		}
		for _, installation := range installations {
			if installationTenancyAllowed(w.envelope, installation.PrincipalID, installation.OrgID) {
				continue
			}
			// An installation acknowledged between the classification and
			// this transaction is found here and nowhere else. Revoking it is
			// a narrowing, so the write that was classified a pure grant
			// stops and is taken again under the witness.
			if !witnessed {
				return errModuleEnvelopeNarrowsAfterAll
			}
			if err := s.uninstallSolutionTx(ctx, w.actorID, w.actorType, installation.OrgID, installation.ID); err != nil {
				return err
			}
		}
		return w.audit(ctx)
	}
	// The policy log witnesses NARROWINGS — "a narrowing nothing witnessed is
	// one a restore undoes silently". A write that only grants (a first
	// envelope, a principal added, nothing an installation loses)
	// is not one: a restore that undoes it leaves less authority, which is the
	// fail-closed direction, so it is applied and audited without a witness.
	// Anything that removes or changes what was granted is witnessed as today.
	narrows, err := s.moduleEnvelopeNarrows(ctx, w.envelope, w.record.Document)
	if err != nil {
		return err
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
		return err
	}
	// This replica fences its registry at once — it serves nothing derived
	// from the replaced ceiling — and asks for the pass that derives the new
	// one. Every other replica fences at its next authority decision, which
	// reads the committed revision (fenceModuleRegistrySnapshot).
	s.observeModuleEnvelopeRevision(w.record.Revision)
	s.requestRegistryPass()
	return nil
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
	if snapshot.manifestDerived {
		if err := s.fenceManifestGenerations(ctx, snapshot); err != nil {
			return err
		}
	}

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
		unapplied, err = counter.CountUnappliedManifests(ctx)
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
// A pure grant is exactly this, against the envelope in force: every principal
// is still there and holds at least what it held (moduleEnvelopeOnlyAdds), and
// no installation loses its organization. What may differ is the revision,
// whole principals that were added, and authority added to a principal that
// was kept.
//
// The tenant is the one addition that is also a narrowing — a tenant set
// removes every other organization — which is why apply re-checks every active
// installation against the new ceiling (errModuleEnvelopeNarrowsAfterAll)
// rather than trusting this classification alone.
//
// A host with no envelope has granted each applied manifest what it asks for
// (the ceiling is the manifest), so a first envelope over any manifest that
// asks for authority is a narrowing; over none, it narrows nothing and only
// the installations are checked.
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
		if current == nil {
			applied, err := s.store.ListSolutionHostBindings(ctx)
			if err != nil {
				return err
			}
			if len(AppliedAuthorityRequests(applied)) > 0 {
				narrows = true
				return nil
			}
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

// moduleEnvelopeOnlyAdds reports that after keeps every principal before holds,
// each with at least the authority it had (moduleGrantOnlyGains), and may add
// principals. A document it cannot decode as an envelope, a key the envelope
// does not have among them, is not an addition.
//
// The documents are decoded, not validated: the revision in force was
// validated when it was written, the next one before this is asked, and a rule
// that later tightened is the registry derivation's to apply, not this
// classification's to refuse.
func moduleEnvelopeOnlyAdds(before, after []byte) bool {
	was, ok := decodeModuleEnvelopeWire(before)
	if !ok {
		return false
	}
	is, ok := decodeModuleEnvelopeWire(after)
	if !ok {
		return false
	}
	for prefix, principal := range was.Principals {
		kept, exists := is.Principals[prefix]
		if !exists || !moduleGrantOnlyGains(principal.Grant.grant(prefix), kept.Grant.grant(prefix)) {
			return false
		}
	}
	return true
}

func decodeModuleEnvelopeWire(document []byte) (moduleEnvelopeWire, bool) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var wire moduleEnvelopeWire
	if err := decoder.Decode(&wire); err != nil || decoder.More() {
		return moduleEnvelopeWire{}, false
	}
	return wire, true
}

// moduleGrantOnlyGains reports that a kept principal loses nothing: what an
// installation may hold of it (its queues, namespaces, resources, read and
// operation audiences, artifact approvals) fits within what it holds next, by
// the one rule this host already holds an installation's consent to its
// ceiling with (InstallationScopeWithin); its tenant is unchanged and a
// cross-tenant grant is kept; and every binding it had keeps its delegation
// mode.
//
// Reusing that rule is what lets a gain be told from a change without a second
// copy of the envelope's semantics: a resource kind added to an allowlist, an
// action added to a binding's scope, a binding's resource ids widened or
// dropped (every resource) are gains there, so they are gains here; a kind
// removed, an audience retargeted, resource ids appearing on a scope that had
// none, or an artifact approval changed are not, there or here.
//
// The delegation mode is outside that rule because it is not authority an
// installation consents to but how the binding's credential is exchanged:
// content_read restricts ordinary owner-only exchanges, so any change to it is
// witnessed.
func moduleGrantOnlyGains(was, is ModulePrincipalGrant) bool {
	if was.Tenant != is.Tenant || was.CrossTenant && !is.CrossTenant {
		return false
	}
	for id, binding := range was.OperationAudiences {
		if kept, ok := is.OperationAudiences[id]; !ok || kept.DelegationMode != binding.DelegationMode {
			return false
		}
	}
	return InstallationScopeWithin(FullInstallationScope(was), FullInstallationScope(is))
}
