package business

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/codefly-dev/core/solutionhost"
)

// The DELIVERED module envelope: how the composition, rather than a person,
// states the ceiling a host holds its modules to.
//
// The composition declares each environment's envelope; its publish signs it
// with the cell's release key, and a delivery Job hands the carrier to this
// host through the same signed delivery channel, verifier and domain pins a
// module manifest uses. Without it a hosted cell receives every module's signed
// manifest and grants none of them, because nothing writes the ceiling the
// registry is derived inside.
//
// The interactive write stays (PutModuleEnvelope): a platform super_admin with
// fresh MFA can still write a higher revision. The two share one writer, so a
// delivered revision is classified, witnessed, fenced and revoked exactly like
// an interactive one, and neither relaxes the other.

// ErrSolutionDeliverySignerNotRelease: the envelope verifies under a key this
// host pins as a person's. A developer's key may deliver a manifest it is
// pinned for; it never states what a module may do on a host. 403, terminal.
var ErrSolutionDeliverySignerNotRelease = errors.New("module envelope is not signed by a release key this host pins")

// moduleEnvelopeDeliveryActor is who a delivered revision is attributed to.
// No person wrote it, so it is the system actor the audit layer records every
// automated mutation as (actor_type "system", no user id); the release key
// that signed it is named in the event's payload, and the same actor revokes
// whatever installation a delivered narrowing takes away.
const moduleEnvelopeDeliveryActor = "system"

// ModuleEnvelopeDeliveryReceipt is what the host did with a delivered envelope.
type ModuleEnvelopeDeliveryReceipt struct {
	Disposition SolutionDeliveryDisposition
	Revision    uint64
	// Digest is core's digest of the canonical payload; Signer the pinned
	// release key id that signed it.
	Digest string
	Signer string
}

// SetModuleEnvelopeDeliveryHost names the module this host was composed as,
// whose delivery account carries the host's envelope. Boot wiring; empty
// leaves envelope delivery refusing every carrier.
func (s *Service) SetModuleEnvelopeDeliveryHost(module string) {
	s.hostModule = module
}

// ReceiveModuleEnvelopeDelivery verifies, authorises, admits and writes one
// delivered module envelope carrier.
//
// The ORDER is the contract, and it is signature-first like
// ReceiveSolutionDelivery:
//
//  1. parse the carrier with core's strict ParseSigned;
//  2. verify it with core's VerifyDeliveredEnvelope: the pinned keys, the
//     canonical envelope, a RELEASE signer, its pin for the envelope's domain;
//  3. authorise the CALLER as the delivery account of this host's own
//     namespace — the envelope is the host's document, not any one module's;
//  4. hold it to the host's rules: it names no organization, and every grant
//     is one the interactive write would accept (ParseModuleEnvelope);
//  5. admit its revision against the latest DELIVERED revision (core's
//     AdmitEnvelope), then above the committed one whoever wrote it;
//  6. write it through the one writer PutModuleEnvelope uses.
func (s *Service) ReceiveModuleEnvelopeDelivery(
	ctx context.Context, raw []byte, credential string,
) (*ModuleEnvelopeDeliveryReceipt, error) {
	if s.deliveryVerifier == nil || s.deliveryStore == nil || s.deliveryCarrier == nil || s.deliveryCoordinate == "" ||
		s.moduleEnvelopes == nil || s.hostModule == "" {
		return nil, fmt.Errorf("%w: module envelope delivery is not fully wired on this host", ErrSolutionDeliveryUnattested)
	}
	signed, err := solutionhost.ParseSigned(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
	}
	delivered, err := solutionhost.VerifyDeliveredEnvelope(ctx, signed, s.deliveryVerifier)
	if errors.Is(err, solutionhost.ErrEnvelopeSignerNotRelease) {
		return nil, fmt.Errorf("%w: %w", ErrSolutionDeliverySignerNotRelease, err)
	}
	if err != nil {
		return nil, classifySolutionDeliveryVerification(err)
	}
	candidate, err := delivered.Envelope()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
	}
	if err := s.deliveryCarrier.AuthorizeCarrier(ctx, credential, s.moduleNamespaces.Namespace(s.hostModule)); err != nil {
		return nil, err
	}
	document, err := deliveredModuleEnvelopeDocument(candidate)
	if err != nil {
		return nil, err
	}
	envelope, err := ParseModuleEnvelope(document)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
	}
	if err := refuseDeliveredTenantPins(envelope); err != nil {
		return nil, err
	}
	digest, err := candidate.Digest()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
	}
	signer := delivered.Signer().KeyID
	receipt := &ModuleEnvelopeDeliveryReceipt{Revision: candidate.Revision, Digest: digest, Signer: signer}

	var previous *ModuleEnvelopeRecord
	var committed uint64
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		if previous, err = s.moduleEnvelopes.LatestDeliveredModuleEnvelopeRecord(ctx); err != nil {
			return err
		}
		committed, err = s.moduleEnvelopes.LatestModuleEnvelopeRevision(ctx)
		return err
	}); err != nil {
		return nil, fmt.Errorf("read the module envelope: %w", err)
	}
	var previousEnvelope *solutionhost.ModuleEnvelope
	if previous != nil {
		if previousEnvelope, err = solutionhost.EnvelopeFromVerified(previous.Payload); err != nil {
			return nil, fmt.Errorf("%w: delivered module envelope revision %d no longer reads: %w",
				ErrSolutionDeliveryHostState, previous.Revision, err)
		}
	}
	decision, err := solutionhost.AdmitEnvelope(s.deliveryCoordinate, previousEnvelope, candidate)
	if err != nil {
		return nil, classifySolutionDeliveryAdmission(err)
	}
	if decision == solutionhost.DecisionReplay {
		receipt.Disposition = SolutionDeliveryReplayed
		return receipt, nil
	}
	// Core knows only delivered revisions. The host's own rule: a delivered
	// revision lands above the committed one whatever wrote it, so an
	// administrator's interactive change is never silently replaced by a
	// composition that has not stated it.
	if committed >= candidate.Revision {
		return nil, fmt.Errorf("%w: revision %d was written interactively by a platform administrator; the composition must state that change and deliver a revision above %d",
			ErrSolutionDeliveryConflict, committed, committed)
	}

	record := &ModuleEnvelopeRecord{
		Revision: candidate.Revision, Document: document,
		Signer: signer, OwnershipDomain: candidate.Host.Domain,
		Payload: delivered.Payload(), Envelope: bytes.Clone(signed.Envelope),
	}
	// A delivered NARROWING is witnessed like an interactive one: on a host
	// with no policy log it is refused (ErrPolicyLogUnreachable) exactly as
	// PutModuleEnvelope refuses it, and nothing is written. Delivery does not
	// bypass the witness; the composition's envelope waits for the log.
	err = s.writeModuleEnvelope(ctx, moduleEnvelopeWrite{
		envelope: envelope, record: record, expectedRevision: committed,
		actorID: moduleEnvelopeDeliveryActor, actorType: ActorTypeSystem,
		audit: func(ctx context.Context) error {
			return s.emitTx(ctx, moduleEnvelopeDeliveryActor, ActorTypeSystem, EventModuleEnvelopeDelivered, "module_envelope",
				strconv.FormatUint(record.Revision, 10), "", map[string]any{
					"revision":          int64(record.Revision), //nolint:gosec // a revision is a small positive counter
					"previous_revision": int64(committed),       //nolint:gosec // a revision is a small positive counter
					"principals":        len(envelope.Principals),
					"signer":            signer,
					"ownership_domain":  record.OwnershipDomain,
					"digest":            digest,
				})
		},
	})
	if errors.Is(err, ErrModuleEnvelopeConflict) {
		// Another writer committed between admission and the append. A retry
		// is decided against what it wrote, so it is the caller's to make.
		return nil, fmt.Errorf("the committed module envelope moved while revision %d was admitted; deliver it again: %w", record.Revision, err)
	}
	if err != nil {
		return nil, err
	}
	receipt.Disposition = SolutionDeliveryAccepted
	return receipt, nil
}

// refuseDeliveredTenantPins holds a delivered envelope to the one rule that is
// the host's and not core's: it NAMES NO ORGANIZATION.
//
// It reads the PARSED envelope, deliberately. Looking the key up in the raw
// grant made this a second decoder of a field ParseModuleEnvelope already
// decodes, and the two disagreed about the only thing the rule is about:
// encoding/json falls back to a case-insensitive field match, which
// DisallowUnknownFields does not interrupt because a field was found, so a
// grant carrying "Tenant" passed the raw lookup and was then parsed, stored
// and applied as a tenant pin. The consequence was not cosmetic — a pinned
// tenant makes moduleAvailableToOrganization false everywhere else, so the
// delivery revoked every active installation of that module in every other
// organization, from one capital letter in a rendered envelope.
func refuseDeliveredTenantPins(envelope *ModuleEnvelope) error {
	principals := make([]string, 0, len(envelope.Principals))
	for principal := range envelope.Principals {
		principals = append(principals, principal)
	}
	sort.Strings(principals)
	for _, principal := range principals {
		if tenant := envelope.Principals[principal].Grant.Tenant; tenant != "" {
			return fmt.Errorf("%w: principal %q pins tenant %q: a delivered module envelope names no organization: an organization id is this host's own record; tenancy resolves through each organization's installation, and a pin to one organization is written interactively by a platform super_admin at a higher revision",
				ErrSolutionDeliveryRefused, principal, tenant)
		}
	}
	return nil
}

// deliveredModuleEnvelopeDocument is the host's envelope document for a
// delivered one: `{"revision", "principals": {p: {"grant": g}}}`, each grant
// as core carried it.
//
// A delivered envelope NAMES NO ORGANIZATION. An organization id is this
// host's own record, which no composition can know before the organization
// exists; a delivered grant's tenancy therefore resolves through each
// organization's installation (the shared tenancy, or cross_tenant as
// granted). A pin to one organization is an interactive super_admin write.
func deliveredModuleEnvelopeDocument(candidate *solutionhost.ModuleEnvelope) ([]byte, error) {
	principals := make([]string, 0, len(candidate.Principals))
	for principal := range candidate.Principals {
		principals = append(principals, principal)
	}
	// Not ordered: the wire map is json.Marshal's, which sorts its own keys.
	wire := struct {
		Revision   uint64                     `json:"revision"`
		Principals map[string]json.RawMessage `json:"principals"`
	}{Revision: candidate.Revision, Principals: make(map[string]json.RawMessage, len(principals))}
	for _, principal := range principals {
		grant := candidate.Principals[principal]
		section, err := json.Marshal(struct {
			Grant json.RawMessage `json:"grant"`
		}{Grant: grant})
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
		}
		wire.Principals[principal] = section
	}
	return json.Marshal(wire)
}
