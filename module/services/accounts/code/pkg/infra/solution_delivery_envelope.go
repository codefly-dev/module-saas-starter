package infra

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/codefly-dev/core/solutionhost"
)

// The authority ENVELOPE: the ceiling a delivered authority document is held
// against (issue #952).
//
// It is read from the trust anchor, and the two reasons are the same two reasons
// the identity allowlist is read from there.
//
// First, A DOCUMENT MUST NEVER CARRY ITS OWN CEILING. core says so in as many
// words — "an envelope a document carried would be a document declaring its own
// ceiling" — and the consequence is not abstract: ValidateAgainst tests a
// document's approved build and its (principal, binding) grants against the
// envelope's, so an envelope assembled from the delivery tree answers itself for
// every document in it.
//
// Second, it must not be reachable by whoever can set this service's
// environment. Workspace environment is delivered by the COMPOSITION, so a
// configurable ceiling path is a ceiling the composition chooses — the same hole
// SolutionHostTrustAnchorPath exists to close, one axis over. A platform
// administrator writes the ceiling and a platform-owned delivery path mounts it;
// neither delivery writer can.
//
// ABSENCE AND DAMAGE ARE DIFFERENT. A host with no envelope delivered has no
// ceiling, so it activates nothing and says so — authority is simply not a
// question it can answer. A host whose envelope is present and unreadable is a
// host whose ceiling was delivered and cannot be applied, and that refuses the
// boot: reading a damaged ceiling as "no ceiling" would turn a corrupted file
// into a silently narrower system, and reading it as a permissive one would turn
// it into a silently wider one.

// authorityEnvelopeFileName is the ceiling's name inside the trust anchor.
// Fixed, like the other two: the anchor is written by one platform-owned path,
// and a configurable name is one more thing someone could point elsewhere.
const authorityEnvelopeFileName = "authority_envelope.json"

// ErrSolutionAuthorityEnvelopeUnusable reports a ceiling that was delivered and
// cannot be applied.
var ErrSolutionAuthorityEnvelopeUnusable = errors.New("solution authority envelope is unusable")

// solutionAuthorityEnvelopeDocument is the ceiling's wire form.
//
// It is declared here rather than decoded straight into core's Envelope because
// core's type carries no JSON tags — it is a value a host assembles, never a
// document core parses, which is exactly the property that keeps a document from
// carrying one.
type solutionAuthorityEnvelopeDocument struct {
	// Revision is the ceiling's revision. Both halves of an activation tuple
	// must name it, so a document validated against a superseded ceiling cannot
	// activate against the current one.
	Revision uint64 `json:"revision"`

	// Grants are the (principal, binding) pairs this ceiling allows. The PAIR,
	// because a binding is held BY someone: a ceiling listing bindings alone
	// would let a document move one between principals unchanged, and moving a
	// binding is a grant of somebody else's authority.
	Grants []solutionhost.EnvelopeGrant `json:"grants"`

	// ApprovedBuilds are the builds this ceiling has approved.
	ApprovedBuilds []solutionhost.ImageDigest `json:"approvedBuilds"`
}

// imageDigestPattern is the OCI manifest digest shape, restated because a
// readable stand-in for a digest in a ceiling would approve a build that cannot
// exist — and would read as a ceiling that works until something compared it to
// a real one.
var imageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ReadSolutionAuthorityEnvelope reads the ceiling from the trust anchor.
//
// The second return says whether a ceiling is DELIVERED at all. False with no
// error is a host that may activate nothing; an error is a host that must not
// start.
func ReadSolutionAuthorityEnvelope() (solutionhost.Envelope, bool, error) {
	path := filepath.Join(SolutionHostTrustAnchorPath, authorityEnvelopeFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No ceiling delivered. Not an error and not an empty ceiling: the
			// host answers no authority question at all, which is what
			// ActivateDeliveredAuthority refuses by name.
			return solutionhost.Envelope{}, false, nil
		}
		return solutionhost.Envelope{}, false, fmt.Errorf(
			"%w: %s is present on the trust anchor and cannot be read: %w",
			ErrSolutionAuthorityEnvelopeUnusable, path, err)
	}
	document := &solutionAuthorityEnvelopeDocument{}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	// Strict, for the reason the verification policy is strict: a field this
	// host does not model is a version step, not an intention to ignore.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(document); err != nil {
		return solutionhost.Envelope{}, false, fmt.Errorf("%w: %s does not decode: %w",
			ErrSolutionAuthorityEnvelopeUnusable, path, err)
	}
	if err := document.validate(path); err != nil {
		return solutionhost.Envelope{}, false, err
	}
	return solutionhost.Envelope{
		Revision:       document.Revision,
		Grants:         document.Grants,
		ApprovedBuilds: document.ApprovedBuilds,
	}, true, nil
}

// validate refuses a ceiling that would bound less than its author meant.
//
// Every case here is one where the document decodes and reads as a ceiling while
// bounding nothing in particular — which is worse than a missing file, because a
// missing file is reported as "no ceiling" and this would be reported as one.
func (document *solutionAuthorityEnvelopeDocument) validate(path string) error {
	if document.Revision == 0 {
		return fmt.Errorf("%w: %s names no revision, and a tuple that agrees with itself about no ceiling activates nothing",
			ErrSolutionAuthorityEnvelopeUnusable, path)
	}
	for index, grant := range document.Grants {
		switch {
		case strings.TrimSpace(grant.Principal) == "":
			return fmt.Errorf("%w: %s grant %d names no principal; the ceiling is WHO MAY HOLD WHICH, and a grant with no holder bounds half the statement",
				ErrSolutionAuthorityEnvelopeUnusable, path, index)
		case strings.TrimSpace(grant.Binding.ID) == "":
			return fmt.Errorf("%w: %s grant %d names no binding id; a binding id is what a credential seals and a verifier looks up",
				ErrSolutionAuthorityEnvelopeUnusable, path, index)
		case grant.Binding.Revision == 0:
			return fmt.Errorf("%w: %s grant %d binding %q names no revision; containment is exact, so a revision-less entry matches nothing and reads as an entry that works",
				ErrSolutionAuthorityEnvelopeUnusable, path, index, grant.Binding.ID)
		case strings.TrimSpace(grant.Binding.Audience) == "" || strings.TrimSpace(grant.Binding.Scope) == "":
			return fmt.Errorf("%w: %s grant %d binding %q names no audience or no scope",
				ErrSolutionAuthorityEnvelopeUnusable, path, index, grant.Binding.ID)
		}
	}
	for index, build := range document.ApprovedBuilds {
		if !imageDigestPattern.MatchString(string(build)) {
			return fmt.Errorf("%w: %s approved build %d is %q, which is not a SHA-256 OCI image manifest digest",
				ErrSolutionAuthorityEnvelopeUnusable, path, index, build)
		}
	}
	return nil
}
