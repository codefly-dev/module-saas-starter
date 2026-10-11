package adapters

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	coreworkcontext "github.com/codefly-dev/core/workcontext"
	sdkworkcontext "github.com/codefly-dev/sdk-go/workcontext"
)

// The Work Context switch is a HARD CUTOVER, and this is the test that says so
// before someone starts it incrementally.
//
// The host mints and verifies through github.com/codefly-dev/sdk-go/workcontext.
// core/workcontext is the one implementation it must end up on — a host that
// carries its own copy of its dependency's rules owns the copy that drifts, and
// that is the whole reason the switch is owed. The question this pins is not
// WHETHER to switch but whether it can be done one call site at a time, and the
// answer is no, for two reasons that are easy to miss by reading either package
// alone:
//
//  1. THE WIRE FORMATS ARE INCOMPATIBLE AT THE ENCODING, not at a field. sdk-go
//     signs a JSON payload; core signs a deterministic protobuf encoding. core
//     refuses an sdk-go capability as "not a core token" before it reaches any
//     field at all — so there is no dual-read window, no verifier that accepts
//     both, and every capability minted before the cutover is refused after it.
//     That is the behaviour the owner's "no backward compatibility" rule asks
//     for, but it has to be planned as a cold cutover rather than discovered.
//
//  2. MINTING A CORE CAPABILITY IS GATED ON A FIELD THIS HOST CANNOT ANSWER.
//     basev0.WorkContextV1's seal is `(buf.validate.field).required = true`, and
//     WorkSealV1.image_digest is min_len 1 matching ^sha256:[a-f0-9]{64}$. So a
//     core mint needs the digest of the image the authenticated workload is
//     actually running. The mint authenticates with a shared secret: there is no
//     TokenReview and no Kubernetes pod reader in this tree, so there is nothing
//     it can consult about the pod. Filling it from the approved record instead
//     makes the check compare approved against approved — it type-checks, passes
//     for every caller including the superseded pod it exists to refuse, and no
//     test fails. core records that shape in docs/architecture.md as "A required
//     input needs an independent source".
//
// So the switch waits on the mint's workload authentication (core #692's C1 and
// the human-session half of C2, both the owner's), not on a core pin. When it
// lands, this test is DELETED rather than updated: its subject is the gap.
func TestTheWorkContextCutoverIsAtomic(t *testing.T) {
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	signer, err := sdkworkcontext.NewWorkContextSigner(sdkworkcontext.WorkContextSignerOptions{
		Issuer: "saas-starter", KeyID: "k1", PrivateKey: private,
	})
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	token, context, err := signer.StartTask(sdkworkcontext.StartTaskInput{
		Audience: "accounts", TenantID: "t1", OwnerPrincipalID: "p1",
		TaskID: "task1", SessionID: "s1", AuthorizationRevision: 1,
		ReplayPolicy:    sdkworkcontext.WorkContextReplayIdempotent,
		AuthorityScopes: []*basev0.WorkScopeV1{{ResourceKind: "accounts.users", Actions: []string{"read"}}},
		TTL:             time.Minute,
	})
	if err != nil {
		t.Fatalf("mint a capability the way this host mints today: %v", err)
	}

	// The seal core requires is absent, because sdk-go has no concept of one.
	// Asserted so that "sdk-go grew a seal" is a visible change rather than a
	// silent reason this test stops meaning what it says.
	if context.GetSeal() != nil {
		t.Fatal("sdk-go now emits a seal; re-read whether the cutover is still atomic")
	}

	// And core will not read it. Inspect is the WEAKEST core entrypoint — it
	// verifies no signature and holds no trust root — so a refusal here means
	// Verify and Authenticate refuse it too, and no partial switch can route
	// one call site through core while the mint stays where it is.
	_, err = coreworkcontext.Inspect(token.Encoded())
	if err == nil {
		t.Fatal("core READ an sdk-go capability: the formats have converged and the switch may no longer be atomic — re-read this test's premise before trusting it")
	}
	if !strings.Contains(err.Error(), "not a core token") {
		t.Fatalf("core refused the capability for a different reason than the format: %v\n"+
			"This test is about the ENCODING boundary; a refusal on some other ground means the premise moved.", err)
	}

	// NON-VACUITY. Everything above would also pass if Inspect simply refused
	// every string it was handed, which would make this test a tautology about
	// core rather than a statement about the two formats. So core must read
	// core: its own shipped fixtures go through the same call, and at least one
	// has to be readable.
	//
	// Inspect is structural, so an ACCEPTED fixture and a fixture refused for a
	// reason only a verifier could reach (an expired window, a superseded
	// revision, a consumed single-use capability) are both readable here; what
	// must not happen is every one of them failing to decode.
	fixtures, err := coreworkcontext.Fixtures(time.Now())
	if err != nil {
		t.Fatalf("core fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("core shipped no capability fixtures, so nothing proved Inspect reads anything")
	}
	var read int
	for _, fixture := range fixtures {
		if _, err := coreworkcontext.Inspect(fixture.Token); err == nil {
			read++
		}
	}
	if read == 0 {
		t.Fatalf("Inspect read 0 of core's own %d fixtures, so its refusal above says nothing about the sdk-go format", len(fixtures))
	}
	t.Logf("Inspect read %d of core's own %d fixtures and refused the sdk-go capability", read, len(fixtures))
}
