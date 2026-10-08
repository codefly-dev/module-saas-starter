package keyservice

import (
	"fmt"
	"regexp"
	"strings"

	"accounts/pkg/business"
)

// envelopeVersion versions the FRAMING of a stored value — that it names the
// backend which sealed it and that the backend's own payload follows — not the
// key, the algorithm or the ciphertext. A new backend therefore extends the tag
// set rather than the version, and every value already stored keeps reading.
const envelopeVersion = "cfs1"

// Tags the backends write. A tag names the key-management SERVICE rather than
// the configured backend, so a stored value says which service to ask instead
// of leaving a later reader to re-derive it from whatever configuration was in
// force when the value was sealed. The two namespaces are deliberately not the
// same: `kms` selects whichever cloud the deployment runs in, and its
// ciphertexts have to stay distinguishable from another cloud's.
const (
	TagVaultTransit = "vault-transit"
	TagGCPKMS       = "gcp-kms"
)

// Envelope is the stored form of a sealed value.
//
// The tag is what makes a cutover possible: a read resolves the backend from
// the value itself, so a deployment that has switched backends still opens
// everything the previous one sealed, and the previous binding can be withdrawn
// exactly when no stored value names it.
//
// Where the KEY VERSION is recorded is each backend's own business, because
// each already answers it differently. A Vault Transit ciphertext carries its
// own `vault:v<N>:` prefix, so the payload is the ciphertext and nothing is
// added. A Cloud KMS ciphertext is opaque, so that backend's payload names the
// key version ahead of it.
type Envelope struct {
	Backend string
	Payload string
}

func (e Envelope) String() string {
	return envelopeVersion + ":" + e.Backend + ":" + e.Payload
}

// ParseEnvelope reads a stored value. It is strict in both halves: an
// unrecognised version and an empty payload are refused rather than guessed at,
// because the alternative to refusing is handing a backend bytes it will
// mis-read.
func ParseEnvelope(stored string) (Envelope, error) {
	version, rest, found := strings.Cut(stored, ":")
	if !found || version != envelopeVersion {
		return Envelope{}, fmt.Errorf("unsupported secret envelope framing %q, this build opens %s: %w", version, envelopeVersion, business.ErrInvalidSecretEnvelope)
	}
	backend, payload, found := strings.Cut(rest, ":")
	if !found || backend == "" || payload == "" {
		return Envelope{}, fmt.Errorf("invalid secret envelope: %w", business.ErrInvalidSecretEnvelope)
	}
	return Envelope{Backend: backend, Payload: payload}, nil
}

// envelopeFraming matches any application envelope, including a framing version
// this build does not implement.
var envelopeFraming = regexp.MustCompile(`^cfs[1-9][0-9]*:[^:]+:.`)

// IsEnvelopeFraming reports whether a stored value is an application envelope at
// all — as opposed to a pre-envelope plaintext.
//
// It deliberately accepts framing versions this build cannot OPEN. The migration
// sweeps that upgrade pre-envelope plaintext select on "not an envelope", and a
// sweep that recognised only its own version would treat every value written by
// another backend — or by a later framing — as plaintext, re-seal the envelope
// STRING, and store a doubly-wrapped payload nothing can ever read. Refusing to
// open an unknown framing is correct; sweeping it is destruction, so the two
// questions get two functions.
func IsEnvelopeFraming(stored string) bool {
	return envelopeFraming.MatchString(stored)
}

// EnvelopePrefix is the stored-value prefix every envelope one backend seals
// begins with. A migration sweep selects on it, so it has to be the whole
// discriminator and not just the version: a predicate that matched only the
// version would re-seal what the new backend already sealed and store a
// doubly-wrapped payload nothing can read.
func EnvelopePrefix(tag string) string {
	return envelopeVersion + ":" + tag + ":"
}
