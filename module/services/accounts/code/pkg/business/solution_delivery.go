package business

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/codefly-dev/core/solutionhost"
)

// The delivery inbox: how a signed module manifest reaches this host, and why
// it is persisted rather than read from a directory.
//
// A module instance is declared by ONE signed document, core's
// codefly/module-manifest/v1: where it runs, which workloads run as it, where
// its endpoints are and what authority it asks for. Delivery POSTs its carrier;
// the host verifies it on receipt, authorises the caller that handed it over,
// decides it against the generation already applied for its binding, and
// persists the bytes. The reconciler then reads its desired set from durable
// state, which is what makes "delivery stopped talking" and "delivery said
// remove it" different facts. Removal is a withdrawal GENERATION; nothing
// deletes from the inbox.

// SolutionDeliveryRecord is one row of the inbox: one generation of one
// binding's manifest, with the attestation over exactly those bytes.
//
// The payload and its envelope travel together and are stored together. A
// payload without its envelope is a document nobody can re-verify.
type SolutionDeliveryRecord struct {
	// Binding is the manifest's binding id.
	Binding    string
	Generation uint64
	// ContentHash is sha256 of the canonical payload, `sha256:<64 hex>` — the
	// manifest's digest, since the payload is its canonical encoding.
	ContentHash string
	Payload     []byte
	// Envelope is the DSSE envelope verbatim, so reconciliation verifies the
	// same bytes under the current pins.
	Envelope []byte
	// SignerIdentity is the pinned key id the verifier attested, and
	// OwnershipDomain the domain the manifest speaks for, both recorded at
	// receipt so an operator can see who delivered what.
	SignerIdentity  string
	OwnershipDomain string
	ReceivedAt      time.Time
}

// SolutionDeliveryDisposition is what the host did with a carrier.
type SolutionDeliveryDisposition string

const (
	// SolutionDeliveryAccepted: verified, admitted against the applied
	// generation, and now durable desired state.
	SolutionDeliveryAccepted SolutionDeliveryDisposition = "accepted"
	// SolutionDeliveryReplayed: this exact generation is already durable.
	// Writes nothing.
	SolutionDeliveryReplayed SolutionDeliveryDisposition = "replayed"
)

// Delivery refusals. Each is a distinct fact with a distinct response code,
// because a retry is right for some and wrong for the rest; the adapter maps
// them and the wrapped core error names the cause.
var (
	// ErrSolutionDeliveryMalformed: the carrier does not parse, or its payload
	// is not the canonical encoding of a valid manifest of the one schema this
	// host reads. 400, terminal.
	ErrSolutionDeliveryMalformed = errors.New("module manifest carrier is malformed")

	// ErrSolutionDeliveryUnattested: the envelope does not verify under any
	// key this host pins. 401, terminal.
	ErrSolutionDeliveryUnattested = errors.New("module manifest is not signed by a key this host pins")

	// ErrSolutionDeliveryDomainNotGranted: the signer is pinned, but not for
	// the ownership domain the manifest speaks for, or the binding was applied
	// under another domain. 403, terminal.
	ErrSolutionDeliveryDomainNotGranted = errors.New("module manifest speaks for an ownership domain its signer or binding does not hold")

	// ErrSolutionDeliveryConflict: the generation is older than the applied
	// one, rewrites a delivered one, or would bring a withdrawn binding back.
	// 409, terminal: publish never produces one, so it is a stale or
	// hand-edited delivery.
	ErrSolutionDeliveryConflict = errors.New("module manifest generation conflicts with this binding's history")

	// ErrSolutionDeliveryRefused: a valid, attested manifest this host will not
	// take — another host's, or one whose route alias is reserved or held by
	// another binding. 422, terminal.
	ErrSolutionDeliveryRefused = errors.New("module manifest is refused by this host")

	// ErrSolutionDeliveryHostState: the binding's applied record on this host
	// cannot be read back. 503: the host's own state, which an operator
	// repairs; the delivery is not at fault.
	ErrSolutionDeliveryHostState = errors.New("this host's applied record for the binding is unusable")
)

// SolutionDeliveryStore is the inbox's persistence.
type SolutionDeliveryStore interface {
	// RecordDeliveredDocument inserts one received carrier. It reports whether
	// the row was new: an exact replay writes nothing and is not an error. A
	// generation already durable with different bytes answers
	// ErrSolutionDeliveryConflict.
	RecordDeliveredDocument(ctx context.Context, record *SolutionDeliveryRecord) (inserted bool, err error)

	// ListNewestDeliveredDocuments returns the newest generation per binding —
	// the desired set.
	ListNewestDeliveredDocuments(ctx context.Context) ([]*SolutionDeliveryRecord, error)

	// ReadDeliveredDocument retrieves one exact generation, or nil.
	ReadDeliveredDocument(ctx context.Context, binding string, generation uint64) (*SolutionDeliveryRecord, error)
}

// SolutionDeliveryCounter is the inbox's size, where the store can answer it.
//
// Optional, and asked for by assertion: a store that cannot count leaves the
// registry with no marker, and a mint then answers exactly as it did before
// the marker existed.
type SolutionDeliveryCounter interface {
	// CountDeliveredDocuments is the number of delivered manifests. The inbox
	// is append-only, so it never decreases.
	CountDeliveredDocuments(ctx context.Context) (uint64, error)
	// CountUnappliedManifests is the number of bindings whose newest delivered
	// manifest is newer than what they applied and not refused.
	CountUnappliedManifests(ctx context.Context) (uint64, error)
}

// SolutionDeliveryCarrierAuthorizer decides whether the CALLER may hand this
// host a manifest.
//
// Separate from signature verification, and keeping them apart is the point:
// the envelope says who SIGNED the content; this says who HANDED IT OVER. A
// valid signature does not make every caller a carrier — otherwise anyone who
// captured a carrier could re-deliver it.
type SolutionDeliveryCarrierAuthorizer interface {
	// AuthorizeCarrier authorises the caller's credential as the delivery
	// account of namespace: the namespace the cell runs the manifest's module
	// in, by rule (ModuleNamespaceRule).
	AuthorizeCarrier(ctx context.Context, credential, namespace string) error
}

// ReceiveSolutionDelivery verifies, authorises, admits and persists one
// delivered module manifest carrier.
//
// The ORDER is the contract, and it is signature-first:
//
//  1. parse the carrier with core's strict ParseSigned;
//  2. verify the envelope against the pinned keys, the payload's canonical
//     encoding, and the signer's pin for the manifest's ownership domain
//     (core's VerifyDelivered);
//  3. authorise the CALLER: the delivery account of the module's namespace —
//     the manifest's module, or for a withdrawal the module of the binding's
//     previously applied manifest;
//  4. admit the generation against the binding's applied manifest (core's
//     Admit: stale, rewritten, withdrawn, another host or domain) and its
//     route alias against every applied manifest (core's CheckAlias);
//  5. hold it to this host's own rules — one workload, one manifest; a
//     principal and a publisher stay with their domain; an endpoint is its
//     workload's in-cluster Service (checkManifestAgainstHost);
//  6. persist.
//
// Verification comes before carrier authorisation so an unauthenticated
// caller's malformed bytes are refused for being malformed, and a captured
// carrier replayed by an authorised one is refused for what it is.
func (s *Service) ReceiveSolutionDelivery(
	ctx context.Context, raw []byte, credential string,
) (SolutionDeliveryDisposition, *SolutionDeliveryRecord, error) {
	if s.deliveryVerifier == nil || s.deliveryStore == nil || s.deliveryCarrier == nil || s.deliveryCoordinate == "" {
		// Fail closed and say so. A deployment that wired the endpoint without
		// a verifier or a coordinate must not accept documents.
		return "", nil, fmt.Errorf("%w: the delivery endpoint is not fully wired on this host", ErrSolutionDeliveryUnattested)
	}
	signed, err := solutionhost.ParseSigned(raw)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
	}
	delivered, err := solutionhost.VerifyDelivered(ctx, signed, s.deliveryVerifier)
	if err != nil {
		return "", nil, classifySolutionDeliveryVerification(err)
	}
	manifest, err := delivered.Manifest()
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
	}

	records, err := s.ListSolutionHostBindings(ctx)
	if err != nil {
		return "", nil, err
	}
	var record *SolutionHostBindingRecord
	for _, candidate := range records {
		if candidate.BindingID == manifest.Binding {
			record = candidate
		}
	}
	previous, previousErr := record.AppliedManifest()

	// The carrier is the delivery account of the module's namespace. A
	// withdrawal is delivered from where the binding was delivered from, so
	// its module is the previously applied one.
	module := manifest.Alias
	if manifest.Removed && previous != nil {
		module = previous.Alias
	}
	if err := s.deliveryCarrier.AuthorizeCarrier(ctx, credential, s.moduleNamespaces.Namespace(module)); err != nil {
		return "", nil, err
	}

	if previousErr != nil {
		return "", nil, classifySolutionDeliveryAdmission(previousErr)
	}
	if _, err := solutionhost.Admit(s.deliveryCoordinate, previous, manifest); err != nil {
		return "", nil, classifySolutionDeliveryAdmission(err)
	}
	if err := record.checkOwnershipDomain(manifest); err != nil {
		return "", nil, err
	}
	// The alias is held to the reserved namespaces and to every other binding
	// as it stands once its newest delivered generation applies, so a
	// withdrawal already delivered releases the alias it held.
	newest, err := s.newestDeliveredManifests(ctx, manifest.Binding)
	if err != nil {
		return "", nil, err
	}
	holders := aliasHolders(appliedManifests(records), newest)
	if err := solutionhost.CheckAlias([]string{SolutionHostReservedRouteNamespace}, holders, manifest); err != nil {
		return "", nil, classifySolutionDeliveryAdmission(err)
	}
	// The workloads, principal, publisher and endpoint addresses are held to
	// this host's rules against every other binding as it stands once its
	// newest delivered generation applies — so a second claim is refused here,
	// naming the binding that holds it, rather than found by a pass.
	if err := s.moduleNamespaces.checkManifestAgainstHost(manifest, standingManifests(appliedManifests(records), newest)); err != nil {
		return "", nil, err
	}

	digest, err := manifest.Digest()
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
	}
	stored := &SolutionDeliveryRecord{
		Binding:         manifest.Binding,
		Generation:      manifest.Generation,
		ContentHash:     digest,
		Payload:         delivered.Payload(),
		Envelope:        signed.Envelope,
		SignerIdentity:  delivered.Signer().KeyID,
		OwnershipDomain: manifest.Host.Domain,
		ReceivedAt:      time.Now().UTC(),
	}
	var inserted bool
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var storeErr error
		inserted, storeErr = s.deliveryStore.RecordDeliveredDocument(ctx, stored)
		return storeErr
	}); err != nil {
		return "", nil, err
	}
	if inserted {
		// A new generation changes what the host applies and which module
		// principals the registry holds, so a pass is requested now rather
		// than at the next tick. An exact replay changes nothing.
		s.requestRegistryPass()
		return SolutionDeliveryAccepted, stored, nil
	}
	return SolutionDeliveryReplayed, stored, nil
}

// newestDeliveredManifests is the newest delivered manifest of every binding
// but except, keyed by binding: the desired state an alias is judged against.
// The rows were verified on receipt; one that no longer reads claims nothing
// here, and the pass reports it against its binding.
func (s *Service) newestDeliveredManifests(ctx context.Context, except string) (map[string]*solutionhost.ModuleManifest, error) {
	var records []*SolutionDeliveryRecord
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		records, err = s.deliveryStore.ListNewestDeliveredDocuments(ctx)
		return err
	}); err != nil {
		return nil, fmt.Errorf("read the delivery inbox: %w", err)
	}
	newest := make(map[string]*solutionhost.ModuleManifest, len(records))
	for _, record := range records {
		if record.Binding == except {
			continue
		}
		if manifest, err := solutionhost.ManifestFromVerified(record.Payload); err == nil {
			newest[record.Binding] = manifest
		}
	}
	return newest, nil
}

// SetSolutionDeliveryCoordinate names the host coordinate this host answers
// for, which every admission is decided under. Boot wiring, beside
// SetSolutionDelivery.
func (s *Service) SetSolutionDeliveryCoordinate(coordinate string) {
	s.deliveryCoordinate = coordinate
}

// classifySolutionDeliveryVerification separates "the signer is wrong" from
// "the bytes are wrong", because the two have different response codes and a
// caller acts differently on each.
func classifySolutionDeliveryVerification(err error) error {
	switch {
	case errors.Is(err, solutionhost.ErrWrongDomain):
		return fmt.Errorf("%w: %w", ErrSolutionDeliveryDomainNotGranted, err)
	case errors.Is(err, solutionhost.ErrUnsigned):
		return fmt.Errorf("%w: %w", ErrSolutionDeliveryUnattested, err)
	}
	return fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
}

// classifySolutionDeliveryAdmission maps core's admission refusals — of a
// manifest (Admit, CheckAlias) or an envelope (AdmitEnvelope) — onto this
// host's delivery taxonomy, keeping core's own wording as the cause.
func classifySolutionDeliveryAdmission(err error) error {
	switch {
	case errors.Is(err, solutionhost.ErrStaleGeneration),
		errors.Is(err, solutionhost.ErrRewrittenGeneration),
		errors.Is(err, solutionhost.ErrWithdrawn),
		errors.Is(err, solutionhost.ErrEnvelopeStaleRevision),
		errors.Is(err, solutionhost.ErrEnvelopeRewrittenRevision):
		return fmt.Errorf("%w: %w", ErrSolutionDeliveryConflict, err)
	case errors.Is(err, solutionhost.ErrWrongDomain):
		return fmt.Errorf("%w: %w", ErrSolutionDeliveryDomainNotGranted, err)
	case errors.Is(err, solutionhost.ErrAppliedUnusable):
		return fmt.Errorf("%w: %w", ErrSolutionDeliveryHostState, err)
	case errors.Is(err, solutionhost.ErrInvalid), errors.Is(err, solutionhost.ErrSchema),
		errors.Is(err, solutionhost.ErrEnvelopeInvalid):
		return fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
	}
	// Another host's coordinate, a reserved or held route alias.
	return fmt.Errorf("%w: %w", ErrSolutionDeliveryRefused, err)
}

// DeliveredSolutionHostBindings is the reconciler's source, reading the newest
// generation per binding from the durable inbox.
//
// A document whose apply failed stays in the desired set and is retried on the
// next pass, because the desired set is what the host was TOLD rather than
// what a directory still happens to contain.
type DeliveredSolutionHostBindings struct {
	service *Service
}

// NewDeliveredSolutionHostBindings builds the durable source.
func NewDeliveredSolutionHostBindings(service *Service) *DeliveredSolutionHostBindings {
	return &DeliveredSolutionHostBindings{service: service}
}

// Documents returns the newest delivered generation per binding, as carriers.
//
// Each row is handed back as the carrier it arrived as, so the reconciler
// re-verifies it on every pass rather than trusting the inbox: a key unpinned
// since stops keeping its manifests applied.
//
// An unreadable inbox is an ERROR, not an empty desired set: removal is a
// withdrawal generation precisely so that losing the input can never be
// reconciled as "remove everything".
func (d *DeliveredSolutionHostBindings) Documents(ctx context.Context) ([]SolutionHostBindingDocument, error) {
	var records []*SolutionDeliveryRecord
	if err := d.service.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		records, err = d.service.deliveryStore.ListNewestDeliveredDocuments(ctx)
		return err
	}); err != nil {
		return nil, fmt.Errorf("read the delivery inbox: %w", err)
	}
	documents := make([]SolutionHostBindingDocument, 0, len(records))
	for _, record := range records {
		carrier, err := solutionhost.MarshalSigned(carrierOf(record))
		if err != nil {
			// A row that cannot be put back into a carrier is a defect in what
			// was stored, and it must not read as "delivery withdrew this".
			return nil, fmt.Errorf("rebuild the carrier for %s generation %d: %w",
				record.Binding, record.Generation, err)
		}
		documents = append(documents, SolutionHostBindingDocument{
			Source: fmt.Sprintf("inbox:%s@%d", record.Binding, record.Generation),
			Data:   carrier,
		})
	}
	return documents, nil
}

// carrierOf rebuilds a stored row's signed carrier.
func carrierOf(record *SolutionDeliveryRecord) *solutionhost.Signed {
	return &solutionhost.Signed{Document: record.Payload, Envelope: record.Envelope}
}
