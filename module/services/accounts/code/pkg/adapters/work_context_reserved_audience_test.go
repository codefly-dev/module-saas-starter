package adapters

import (
	"context"
	"regexp"
	"strings"
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Every peer that reads the reserved module audience treats a context carrying
// it as a composed module acting with no person present. A person-driven mint
// that yields that spelling contradicts the premise those peers rely on, even
// where nothing downstream grants it anything — the register's reproducer is an
// audience exchange that succeeds and a module surface that verifies the result.
//
// The refusal is asserted on an UNAUTHENTICATED caller on purpose: it must come
// before the caller's authority is resolved, so the reserved audience is
// unreachable rather than merely unreachable-for-this-person.
//
// These are the four mints a signed-in person reaches with an audience of their
// choosing. The other two entry points that read an audience are refused by the
// same gate but cannot be reached this way, and each for a reason that is the
// point rather than a gap:
//
//   - StartInstallationTask opens with requireInternalCredential, so it is not a
//     person-driven mint at all — an unauthenticated caller is turned away
//     before any audience is looked at.
//   - RenewWorkContext gates the RESOLVED audience, after authenticating: a
//     renewal naming none inherits the parent's, and it is that value which is
//     checked and stamped. Gating the request field instead would let a renewal
//     carry the reserved spelling forward from a parent while naming nothing.
//
// TestEveryRequestSuppliedAudienceRefusesTheReservedOne is what holds those two
// to the gate, from the source, so dropping it from either is still caught.
func TestPersonMintsRefuseTheModuleAudience(t *testing.T) {
	server := &WorkContextAuthorityServer{}
	ctx := context.Background()

	calls := []struct {
		name string
		call func() error
	}{
		{"StartTask", func() error {
			_, err := server.StartTask(ctx, &gen.StartTaskWorkContextRequest{
				OrgId: uuidForReservedAudienceTest, TaskId: uuidForReservedAudienceTest,
				SessionId:        uuidForReservedAudienceTest,
				ActorPrincipalId: uuidForReservedAudienceTest,
				AuthorityScopes:  scopesForReservedAudienceTest(),
				Audience:         ModuleWorkContextAudience,
			})
			return err
		}},
		{"StartRootSession", func() error {
			_, err := server.StartRootSession(ctx, &gen.StartRootSessionWorkContextRequest{
				OrgId: uuidForReservedAudienceTest, ParentWorkContextToken: "parent.token",
				SessionId: uuidForReservedAudienceTest,
				Audience:  ModuleWorkContextAudience,
			})
			return err
		}},
		{"ExchangeAudience", func() error {
			_, err := server.ExchangeAudience(ctx, &gen.ExchangeWorkContextAudienceRequest{
				OrgId: uuidForReservedAudienceTest, ParentWorkContextToken: "parent.token",
				AttenuatedScopes: scopesForReservedAudienceTest(),
				Audience:         ModuleWorkContextAudience,
			})
			return err
		}},
		{"StartChildSession", func() error {
			_, err := server.StartChildSession(ctx, &gen.StartChildSessionWorkContextRequest{
				OrgId: uuidForReservedAudienceTest, ParentWorkContextToken: "parent.token",
				SessionId: uuidForReservedAudienceTest, ActorPrincipalId: uuidForReservedAudienceTest,
				GrantedScopes: scopesForReservedAudienceTest(),
				Audience:      ModuleWorkContextAudience,
			})
			return err
		}},
	}

	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			require.Error(t, err)
			require.Equal(t, codes.PermissionDenied, status.Code(err),
				"the reserved audience must be refused before the caller's authority is resolved")
			require.Contains(t, status.Convert(err).Message(), "never caller-supplied")
		})
	}
}

// The counterpart: an ordinary audience is NOT refused for BEING the reserved
// one, so the test above cannot pass on a server that refuses every audience.
// Such a mint is still refused — by the vocabulary half, which this server has
// no registry to answer from — and that is a different refusal naming a
// different thing, which is the whole distinction being asserted.
func TestAnOrdinaryAudiencePassesTheReservedAudienceGuard(t *testing.T) {
	_, err := (&WorkContextAuthorityServer{}).ExchangeAudience(context.Background(),
		&gen.ExchangeWorkContextAudienceRequest{
			OrgId: uuidForReservedAudienceTest, ParentWorkContextToken: "parent.token",
			AttenuatedScopes: scopesForReservedAudienceTest(),
			Audience:         "modelservice",
		})
	require.Error(t, err)
	require.NotContains(t, status.Convert(err).Message(), "never caller-supplied")
}

// A mint added later must not be able to skip the guard silently. Every
// EXPORTED method of this server that takes its audience FROM THE REQUEST has to
// run the audience gate; the module mints are exempt because they pass the
// audience themselves rather than reading it off a caller's message.
//
// Exported only, because only an exported method is an RPC: the generated
// service interface is what a caller can reach, and it has no unexported
// members. An unexported helper that reads an audience is reached through one of
// these methods, which has already gated, or through host code that builds the
// request itself (exchangeVerifiedParent has one such caller in
// delegated_read_audience.go, where the audience is a derived binding's, not a
// caller's). Matching those too would make this test demand a second gate on a
// path with no caller-supplied value to gate.
func TestEveryRequestSuppliedAudienceRefusesTheReservedOne(t *testing.T) {
	// Comments stripped: a source check that cannot tell code from prose is satisfied
	// by writing the answer in a comment (R1019-N15).
	text := executableSource(t, "work_context_rpcs.go")

	methods := regexp.MustCompile(`(?m)^func \(s \*WorkContextAuthorityServer\) ([A-Z][A-Za-z]*)\(`).
		FindAllStringSubmatchIndex(text, -1)
	require.NotEmpty(t, methods)
	for index, match := range methods {
		name := text[match[2]:match[3]]
		end := len(text)
		if index+1 < len(methods) {
			end = methods[index+1][0]
		}
		body := text[match[0]:end]
		if !strings.Contains(body, "req.GetAudience()") {
			continue
		}
		// Two spellings, both of which gate the request's audience: the field
		// directly, or the local that a renewal resolves it into. Anything else
		// passing through this gate is a value this test cannot vouch for, so it
		// is not accepted as one.
		//
		// Each is matched as a guard whose error LEAVES the handler. Matching the
		// call alone is what let the discard mutation survive (R1019-N15): a call
		// whose result is dropped reads as a check and enforces nothing.
		gatesTheField := regexp.MustCompile(
			guardReturnsItsRefusal(`requireVocabularyAudience\(ctx, req\.GetAudience\(\)\)`)).
			MatchString(body)
		gatesTheResolved := regexp.MustCompile(
			guardReturnsItsRefusal(`requireVocabularyAudience\(ctx, audience\)`)).
			MatchString(body) && strings.Contains(body, "audience := req.GetAudience()")
		require.True(t, gatesTheField || gatesTheResolved,
			"%s takes its audience from the request and must refuse the reserved module "+
				"audience AND return that refusal; a guard whose error is discarded enforces nothing", name)
	}
}

// Every one of these requests is validated before it reaches the guard, so each
// field it declares has to be well-formed: the subject here is the audience, and
// a message rejected for a malformed id would prove nothing about it.
const uuidForReservedAudienceTest = "00000000-0000-4000-8000-000000000001"

func scopesForReservedAudienceTest() []*gen.WorkContextScope {
	return []*gen.WorkContextScope{{ResourceKind: "records", Actions: []string{"read"}}}
}

// Renewal's guard at the top of the method sees only what the REQUEST named, so an
// omitted audience passed it and then inherited the parent's — the one path by which
// a person-driven renewal could still carry the reserved module audience. The
// EFFECTIVE value is what gets signed, so the effective value is what is refused.
func TestR1019RenewInheritedAudience(t *testing.T) {
	text := executableSource(t, "work_context_rpcs.go")

	body := regexp.MustCompile(
		`func \(s \*WorkContextAuthorityServer\) RenewWorkContext\((?s:.*?)\n\}\n`).
		FindString(text)
	require.NotEmpty(t, body)

	inherit := strings.Index(body, "audience = parent.GetAudience()")
	require.NotEqual(t, -1, inherit, "renewal still inherits the parent audience")

	// A refusal of the EFFECTIVE audience, after the inheritance and before signing —
	// and one whose error is RETURNED. Discarding it left the call in place and
	// enforced nothing, which is how this check passed while the guard did not run
	// (R1019-N15).
	returns := regexp.MustCompile(guardReturnsItsRefusal(`requireVocabularyAudience\(ctx, audience\)`))
	located := returns.FindStringIndex(body)
	require.NotNil(t, located,
		"renewal must refuse the audience it will actually sign, not only the one "+
			"requested, and must return that refusal")
	effective := located[0]
	require.Less(t, inherit, effective, "the check must follow the inheritance")

	sign := strings.Index(body, "s.signer.")
	require.NotEqual(t, -1, sign)
	require.Less(t, effective, sign, "the check must precede signing")
}

// guardReturnsItsRefusal is the pattern for "this guard is called AND its error leaves
// the handler". A call whose result is discarded is not a guard, and matching the call
// alone is what let that mutation survive.
//
// The error variable's name is not pinned, because these handlers bind several
// different ones; what is pinned is that something is bound from the call, tested
// against nil, and returned from inside that branch.
func guardReturnsItsRefusal(call string) string {
	return `if \w+ := ` + call + `; \w+ != nil \{\s*return nil, \w+\s*\}`
}
