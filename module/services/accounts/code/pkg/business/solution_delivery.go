package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/codefly-dev/core/solutionhost"
)

// The delivery inbox: how a signed document reaches this host, and why it is
// persisted rather than read from a directory.
//
// A mount is the CURRENT desired set and nothing else. A document that arrived,
// was recorded as desired, failed to apply and then disappeared from the mount
// was never retried: the host had recorded that delivery wanted something and
// had no way to want it again. The persisted representation also dropped the
// carrier, so a restore could not re-verify what it had accepted — only trust
// its own earlier judgement.
//
// So delivery POSTs a carrier, the host verifies it ON RECEIPT, and persists the
// bytes. The reconciler then reads its desired set from durable state, which is
// what makes "delivery stopped talking" and "delivery said remove it" different
// facts. Removal stays a tombstone GENERATION; nothing deletes from the inbox.

// SolutionDeliveryKind is which half of the lifecycle a document is.
//
// The two have different authorised writers and different identities, so they
// are never interchangeable: the kind is part of every lookup and part of the
// carrier authorisation decision.
type SolutionDeliveryKind string

const (
	// SolutionDeliveryPresence declares what runs where.
	SolutionDeliveryPresence SolutionDeliveryKind = "presence"
	// SolutionDeliveryAuthority declares what a module instance is approved for.
	SolutionDeliveryAuthority SolutionDeliveryKind = "authority"
)

// Valid reports whether a kind is one this host serves. Checked at the edge, so
// an unknown kind is a 404 on a path rather than a row with a kind nothing reads.
func (kind SolutionDeliveryKind) Valid() bool {
	return kind == SolutionDeliveryPresence || kind == SolutionDeliveryAuthority
}

// SolutionDeliveryCarrier is one received carrier: the canonical payload and the
// attestation over exactly those bytes.
//
// The two travel together and are stored together. A payload without its bundle
// is a document nobody can re-verify, which is the state that made a restore
// unable to re-check what it had accepted.
type SolutionDeliveryCarrier struct {
	Kind SolutionDeliveryKind
	// Raw is the carrier exactly as delivered — `{schema, document, bundle}` —
	// parsed by core's ParseSigned rather than unmarshalled here. Core's
	// Signed.validate checks the carrier's own schema, which an ad-hoc
	// two-field unmarshal skips: a hand-built carrier claiming any schema used
	// to reach the bundle verifier.
	Raw []byte
}

// SolutionDeliveryRecord is one row of the inbox.
type SolutionDeliveryRecord struct {
	Kind       SolutionDeliveryKind
	DocumentID string
	Generation uint64
	// ContentHash is sha256 of the canonical payload, `sha256:<64 hex>`.
	ContentHash string
	Payload     []byte
	Bundle      []byte
	// SignerIdentity is what the verifier attested, and OwnershipDomain is what
	// the document asserts. Both recorded at receipt so an operator can see who
	// delivered what without re-running verification — and so a later narrowing
	// of the policy reads as a disagreement with history rather than as a silent
	// change in what the host would accept today.
	SignerIdentity  string
	OwnershipDomain string
	ReceivedAt      time.Time
}

// SolutionDeliveryDisposition is what the host did with a carrier. Every value
// maps to exactly one response code, and the mapping is the published contract.
type SolutionDeliveryDisposition string

const (
	// SolutionDeliveryAccepted: verified, new, and now durable desired state —
	// whether or not it has been applied yet. 202.
	SolutionDeliveryAccepted SolutionDeliveryDisposition = "accepted"
	// SolutionDeliveryReplayed: this exact (document, generation, content hash)
	// is already durable. Writes nothing. 200.
	SolutionDeliveryReplayed SolutionDeliveryDisposition = "replayed"
)

// Delivery refusals. Each is a distinct fact with a distinct response code,
// because a retry is right for some and wrong for the rest.
var (
	// ErrSolutionDeliveryMalformed: the carrier does not parse, or the payload
	// is not the canonical encoding of the document it decodes to. 400,
	// terminal. A signer and a host that disagree about which bytes represent
	// the document disagree about what was approved.
	ErrSolutionDeliveryMalformed = errors.New("solution delivery carrier is malformed")

	// ErrSolutionDeliveryUnattested: the bundle does not verify, or its identity
	// is not one this host accepts. 403, terminal.
	ErrSolutionDeliveryUnattested = errors.New("solution delivery carrier is not attested by an accepted signer")

	// ErrSolutionDeliveryDomainNotGranted: the attested signer may not deliver
	// under the ownership domain the document asserts. 403, terminal.
	//
	// Distinct from the above on purpose: the signature is genuine and the
	// signer is accepted, and what failed is the scope of its authority. An
	// operator fixes those two in different places.
	ErrSolutionDeliveryDomainNotGranted = errors.New("attested signer may not deliver under this ownership domain")

	// ErrSolutionDeliveryInvalid: the document parses but Validate refuses it.
	// 422, terminal.
	ErrSolutionDeliveryInvalid = errors.New("solution delivery document is invalid")

	// ErrSolutionDeliveryRewritten: this generation is already durable with
	// DIFFERENT bytes. 409, terminal.
	//
	// It is a conflict rather than a bad request because the operator needs to
	// see it as a rewrite of a delivered generation: publish never rewrites one,
	// so reaching this means a hand-edited delivery tree.
	ErrSolutionDeliveryRewritten = errors.New("solution delivery generation was already delivered with different content")
)

// SolutionDeliveryStore is the inbox's persistence.
type SolutionDeliveryStore interface {
	// RecordDeliveredDocument inserts one received carrier. It reports whether
	// the row was new: an exact replay writes nothing and is not an error.
	//
	// The unique index on (kind, document_id, generation) is what makes a
	// rewrite detectable, so the implementation turns that collision into
	// ErrSolutionDeliveryRewritten rather than overwriting.
	RecordDeliveredDocument(ctx context.Context, record *SolutionDeliveryRecord) (inserted bool, err error)

	// ListNewestDeliveredDocuments returns the newest generation per document
	// for one kind — the desired set.
	ListNewestDeliveredDocuments(ctx context.Context, kind SolutionDeliveryKind) ([]*SolutionDeliveryRecord, error)
}

// SolutionDeliveryCarrierAuthorizer decides whether the CALLER may deliver this
// kind of document.
//
// It is separate from bundle verification, and keeping them apart is the point.
// The bundle says who SIGNED the content; this says who HANDED IT OVER. A valid
// bundle does not make every caller a legitimate carrier — otherwise anyone who
// could replay a captured carrier could re-deliver it, including a superseded
// generation whose signature is still perfectly good.
type SolutionDeliveryCarrierAuthorizer interface {
	// AuthorizeCarrier authorises the caller's credential for this kind, given
	// the namespaces the document's own workloads declare.
	//
	// The namespaces are a parameter because presence and authority are
	// authorised differently: authority is a fixed (service account, namespace)
	// pair, while a presence carrier must come from the namespace the document
	// itself declares. That makes the presence namespace a property of the
	// SIGNED DOCUMENT rather than of host configuration a deployer can edit —
	// the same reason the ownership domain lives inside the canonical bytes.
	AuthorizeCarrier(ctx context.Context, credential string, kind SolutionDeliveryKind, declaredNamespaces []string) error
}

// ReceiveSolutionDelivery verifies, authorises and persists one delivered
// carrier.
//
// The ORDER is the contract, and it is signature-first:
//
//  1. parse the carrier (core's ParseSigned, so the carrier's own schema is
//     checked);
//  2. verify the bundle against the trust root and the identity allowlist;
//  3. check the attested signer may speak for the domain the document asserts;
//  4. authorise the CALLER as a carrier for this kind;
//  5. persist.
//
// Verification comes before carrier authorisation deliberately. The cheaper
// check is the carrier's, so an efficiency argument would put it first — but
// then an unauthenticated caller's malformed bytes would be refused with a
// message about credentials, and a captured carrier replayed by an authorised
// one would be refused with a message about signatures. Ordering by what the
// operator needs to read beats ordering by cost.
func (s *Service) ReceiveSolutionDelivery(
	ctx context.Context, carrier SolutionDeliveryCarrier, credential string,
) (SolutionDeliveryDisposition, *SolutionDeliveryRecord, error) {
	if !carrier.Kind.Valid() {
		return "", nil, fmt.Errorf("%w: unknown delivery kind %q", ErrSolutionDeliveryMalformed, carrier.Kind)
	}
	if s.deliveryVerifier == nil || s.deliveryStore == nil || s.deliveryCarrier == nil {
		// Fail closed and say which half is missing. A deployment that wired
		// the endpoint without a verifier must not accept documents.
		return "", nil, fmt.Errorf("%w: the delivery endpoint is not fully wired on this host", ErrSolutionDeliveryUnattested)
	}

	signed, err := solutionhost.ParseSigned(carrier.Raw)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
	}

	delivered, err := solutionhost.VerifyDelivered(ctx, signed, s.deliveryVerifier)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryUnattested, err)
	}
	document, err := delivered.Document()
	if err != nil {
		// The payload verified but does not decode to a document whose canonical
		// encoding is those same bytes. Malformed rather than unattested: the
		// attestation is sound and the bytes are not what they claim.
		return "", nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
	}
	if err := document.Validate(); err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryInvalid, err)
	}

	// The attested signer must be entitled to the domain the document asserts.
	// Checked here as well as by core's Admit, because a document that will be
	// refused at admission should not become durable desired state in the first
	// place — an inbox of documents nobody may act on is an inbox an operator
	// has to triage.
	if !s.signerMaySpeakFor(delivered.DeliveredBy(), document.OwnershipDomain) {
		return "", nil, fmt.Errorf("%w: signer %q, domain %q",
			ErrSolutionDeliveryDomainNotGranted, delivered.DeliveredBy(), document.OwnershipDomain)
	}

	if err := s.deliveryCarrier.AuthorizeCarrier(
		ctx, credential, carrier.Kind, declaredNamespaces(document)); err != nil {
		return "", nil, err
	}

	payload := delivered.Payload()
	record := &SolutionDeliveryRecord{
		Kind:            carrier.Kind,
		DocumentID:      document.Binding,
		Generation:      document.Generation,
		ContentHash:     solutionDeliveryContentHash(payload),
		Payload:         payload,
		Bundle:          signed.Bundle,
		SignerIdentity:  delivered.DeliveredBy(),
		OwnershipDomain: document.OwnershipDomain,
		ReceivedAt:      time.Now().UTC(),
	}

	var inserted bool
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var storeErr error
		inserted, storeErr = s.deliveryStore.RecordDeliveredDocument(ctx, record)
		return storeErr
	}); err != nil {
		return "", nil, err
	}
	if inserted {
		return SolutionDeliveryAccepted, record, nil
	}
	return SolutionDeliveryReplayed, record, nil
}

// signerMaySpeakFor reports whether an attested signer is granted an ownership
// domain, by the same mapping core's Admit is given.
func (s *Service) signerMaySpeakFor(signer, domain string) bool {
	for _, granted := range s.deliveryDomainsBySigner[signer] {
		if granted == domain {
			return true
		}
	}
	return false
}

// declaredNamespaces is the set of namespaces the document's own workloads name,
// read out of each workload identity's SPIFFE ID.
//
// A presence carrier is authorised against this rather than against a configured
// constant, because a presence Job runs one per module tree in that module's own
// namespace — a fixed value would refuse every genuine carrier. Reading it from
// the signed document also keeps it out of configuration a deployer can edit.
//
// More than one distinct namespace is returned as more than one entry, and the
// authorizer refuses that: the renderer uses one namespace per render and cannot
// emit such a document, so one that exists was hand-built, and "the namespace
// this was delivered from" would have no single answer.
func declaredNamespaces(document *solutionhost.SolutionHostBinding) []string {
	seen := map[string]bool{}
	var namespaces []string
	for _, workload := range document.Workloads {
		namespace := spiffeNamespace(workload.Identity.SPIFFEID)
		if namespace == "" || seen[namespace] {
			continue
		}
		seen[namespace] = true
		namespaces = append(namespaces, namespace)
	}
	return namespaces
}

// spiffeNamespace reads the namespace out of `spiffe://<trust domain>/ns/<ns>/sa/<account>`.
//
// It returns empty rather than guessing when the shape is not that: core
// validates the SPIFFE ID's form, so an ID reaching here in another shape is one
// core accepted and this host does not understand — and inventing a namespace
// from it would authorise a carrier against a name nobody wrote.
func spiffeNamespace(id string) string {
	_, path, found := strings.Cut(id, "://")
	if !found {
		return ""
	}
	segments := strings.Split(path, "/")
	for index := 0; index+1 < len(segments); index++ {
		if segments[index] == "ns" {
			return segments[index+1]
		}
	}
	return ""
}

// solutionDeliveryContentHash is the idempotency key's content half: sha256 over
// the canonical payload, in the `sha256:<64 hex>` shape the schema requires.
func solutionDeliveryContentHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// DeliveredSolutionHostBindings is the reconciler's source, reading the newest
// generation per binding from the durable inbox.
//
// It replaces the mount, and it closes the gap a mount could not: a document
// whose apply failed stays in the desired set and is retried on the next pass,
// because the desired set is what the host was TOLD rather than what a directory
// still happens to contain.
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
// re-verifies it on every pass rather than trusting the inbox. That is
// deliberate and it is what makes a narrowed verification policy take effect: a
// signer removed from the allowlist stops being able to keep its documents
// applied, instead of its last delivery remaining authoritative forever.
//
// An unreadable inbox is an ERROR, not an empty desired set — the same rule the
// mount had, for the same reason: removal is a tombstone generation precisely so
// that losing the input can never be reconciled as "remove everything".
func (d *DeliveredSolutionHostBindings) Documents(ctx context.Context) ([]SolutionHostBindingDocument, error) {
	var records []*SolutionDeliveryRecord
	if err := d.service.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		records, err = d.service.deliveryStore.ListNewestDeliveredDocuments(ctx, SolutionDeliveryPresence)
		return err
	}); err != nil {
		return nil, fmt.Errorf("read the delivery inbox: %w", err)
	}
	documents := make([]SolutionHostBindingDocument, 0, len(records))
	for _, record := range records {
		carrier, err := solutionhost.MarshalSigned(&solutionhost.Signed{
			Schema:   solutionhost.SchemaSignedV1,
			Document: record.Payload,
			Bundle:   record.Bundle,
		})
		if err != nil {
			// A row that cannot be put back into a carrier is a defect in what
			// was stored, and it must not read as "delivery withdrew this".
			return nil, fmt.Errorf("rebuild the carrier for %s generation %d: %w",
				record.DocumentID, record.Generation, err)
		}
		documents = append(documents, SolutionHostBindingDocument{
			Source: fmt.Sprintf("inbox:%s@%d", record.DocumentID, record.Generation),
			Data:   carrier,
		})
	}
	return documents, nil
}
