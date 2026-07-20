package jobs

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"google.golang.org/protobuf/proto"
)

const enqueueFingerprintDomain = "saas.jobs.v1.EnqueueJobRequest\x00"
const replayFingerprintDomain = "saas.jobs.v1.ReplayJobRequest\x00"

// CanonicalOrderingKey renders a collision-free FIFO key. Every opaque
// component is base64url encoded separately; the '.' delimiter cannot appear
// in that alphabet, so ["a", "b"] can never collide with ["a.b"].
func CanonicalOrderingKey(key *jobsv1.JobOrderingKey) (string, error) {
	if key == nil {
		return "", nil
	}
	if err := ValidateCommand(key); err != nil {
		return "", err
	}
	encoded := make([]string, 0, len(key.GetComponents()))
	for _, component := range key.GetComponents() {
		encoded = append(encoded, base64.RawURLEncoding.EncodeToString([]byte(component)))
	}
	canonical := key.GetNamespace() + ":" + strings.Join(encoded, ".")
	if len(canonical) > 255 {
		return "", fmt.Errorf("%w: got %d bytes", ErrOrderingKeyTooLong, len(canonical))
	}
	return canonical, nil
}

// EnqueueFingerprint hashes the deterministic protobuf wire form behind a
// type-specific domain separator. Map insertion order cannot change the result.
func EnqueueFingerprint(request *jobsv1.EnqueueJobRequest) ([sha256.Size]byte, error) {
	return fingerprintCommand(enqueueFingerprintDomain, "enqueue", request)
}

// ReplayFingerprint makes retries of the privileged replay command safe while
// keeping its idempotency namespace distinct from producer enqueue commands.
func ReplayFingerprint(request *jobsv1.ReplayJobRequest) ([sha256.Size]byte, error) {
	return fingerprintCommand(replayFingerprintDomain, "replay", request)
}

// PayloadIdentityMatches preserves the producer identity invariant without
// making exact-payload replay impossible. Original jobs must bind their
// workload identity to the outer idempotency key. A replay copies those payload
// bytes unchanged and carries a new outer key plus replay_of lineage, so the
// original payload identity remains valid by construction.
func PayloadIdentityMatches(envelope *jobsv1.JobEnvelope, payloadIdentity string) bool {
	if envelope == nil || payloadIdentity == "" {
		return false
	}
	return envelope.GetReplayOf() != "" || envelope.GetIdempotencyKey() == payloadIdentity
}

func fingerprintCommand(domain, name string, request proto.Message) ([sha256.Size]byte, error) {
	if err := ValidateCommand(request); err != nil {
		return [sha256.Size]byte{}, err
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(request)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("jobs: marshal %s command: %w", name, err)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write(encoded)
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], hash.Sum(nil))
	return fingerprint, nil
}
