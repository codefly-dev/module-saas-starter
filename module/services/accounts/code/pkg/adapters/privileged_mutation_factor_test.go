package adapters

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SP-IDENT-10: a platform administrator or an organization owner carries out a
// sensitive operation only behind a recent second factor.
//
// requireMFA was made fail-closed for a privileged actor, but the privilege-granting
// and platform-security mutations did not reach it: each ran its authorization guard
// and then its mutation. A helper nothing calls enforces nothing.
func TestR1019PrivilegedMutationRequiresFactor(t *testing.T) {
	// A super-admin with nothing enrolled and no recent factor. requireRoleScope
	// falls to the platform role when no organization is named, so both servers'
	// authorization guards pass and the factor gate is what answers.
	for _, grade := range []string{"support", "billing", "super_admin"} {
		t.Run(grade, func(t *testing.T) {
			installMFAPolicyStore(t, &mfaPolicyStore{platformRole: grade})
			ctx := privilegedActorContext()

			// Counted, not assumed: a grade below every method's role floor would be
			// refused by the authorization guard on every call and prove nothing
			// about the factor gate. At least one call per grade has to reach it.
			reachedTheGate := 0
			for _, call := range privilegedMutations() {
				err := call.invoke(ctx)
				require.Error(t, err, "%s must not mutate without a recent factor", call.name)
				if status.Code(err) == codes.PermissionDenied {
					// This grade is below the method's role floor, so the
					// authorization guard answered first — a stronger refusal, and
					// not what this test is about.
					continue
				}
				require.Equal(t, codes.FailedPrecondition, status.Code(err),
					"%s: expected the factor gate to answer", call.name)
				require.Equal(t, "mfa_required", status.Convert(err).Message(), call.name)
				reachedTheGate++
			}
			require.NotZero(t, reachedTheGate,
				"every call was refused before the factor gate, so this grade proved nothing")
		})
	}
}

// The control: with recent AAL2 evidence the same privileged actor passes the gate,
// so the refusals above are the missing factor and not a handler that refuses
// everything. Asserted on the gate itself rather than through a handler, because a
// handler that passes the gate proceeds into the service and this package's fixtures
// wire no store for the mutations themselves.
func TestR1019PrivilegedMutationAdmittedWithRecentFactor(t *testing.T) {
	installMFAPolicyStore(t, &mfaPolicyStore{platformRole: "super_admin"})
	ctx := stampVerifiedIdentity(context.Background(), mfaActorID, "", auth.Assurance{
		Level:                 auth.AssuranceLevelAAL2,
		AuthenticationMethods: []string{auth.AuthenticationMethodWebAuthn},
		MFAVerifiedAt:         time.Now(),
	})
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, mfaActorID, mfaOrgID)

	require.NoError(t, requireMFA(ctx, mfaActorID),
		"recent AAL2 evidence must satisfy the gate these mutations now carry")
}

// Every method listed here must carry the gate, and carry it BEFORE its mutation: a
// factor check the write has already passed is not a check. Read from the source, so
// a method added to the privileged set cannot be left ungated, and so the ordering
// cannot regress to the shape this finding started as.
//
// Two things this check learned the hard way (R1019-N15). It must prove the gate's
// error is RETURNED, not merely that requireMFA appears — ignoring the result leaves
// the call in place and enforces nothing. And it must read source with comments
// STRIPPED, because a comment containing the expected text otherwise satisfies a
// substring search while the executable guard is gone. Both of those mutations
// survived the first version of this test.
func TestR1019PrivilegedMutationsGateBeforeTheMutation(t *testing.T) {
	// The gate in its only acceptable form: the error is bound, tested, and returned.
	// RE2 has no backreferences, so the three names are captured and compared below —
	// which is what forbids binding one variable and returning another.
	gateReturns := regexp.MustCompile(
		`if (\w+) := requireMFA\(ctx, actorID\); (\w+) != nil \{\s*return nil, (\w+)\s*\}`)

	for _, m := range privilegedMutationMethods {
		t.Run(m.server+"."+m.method, func(t *testing.T) {
			text := executableSource(t, m.file)
			pattern := regexp.MustCompile(`func \(s \*` + m.server + `\) ` + m.method +
				`\(ctx context\.Context(?s:.*?)\n\}\n`)
			body := pattern.FindString(text)
			require.NotEmpty(t, body, "%s.%s not found in %s", m.server, m.method, m.file)

			gate := gateReturns.FindStringSubmatchIndex(body)
			require.NotNil(t, gate,
				"%s.%s must gate a second factor AND return its refusal; a requireMFA whose "+
					"error is discarded enforces nothing", m.server, m.method)
			bound := body[gate[2]:gate[3]]
			tested := body[gate[4]:gate[5]]
			returned := body[gate[6]:gate[7]]
			require.Equal(t, bound, tested,
				"%s.%s tests a different variable than the one requireMFA bound", m.server, m.method)
			require.Equal(t, bound, returned,
				"%s.%s returns a different variable than the one requireMFA bound", m.server, m.method)
			// The MUTATING call specifically, not the first service call in the body:
			// RevokePrincipal reads the principal first to decide which authorization
			// applies, and "before the first service call" would have demanded the gate
			// run before that read — or, read the other way, been satisfied by a gate
			// that merely preceded it.
			call := "service." + m.method + "("
			mutation := strings.Index(body, call)
			require.NotEqual(t, -1, mutation,
				"%s.%s does not call %s; name its mutating call in privilegedMutationMethods",
				m.server, m.method, call)
			require.Less(t, gate[0], mutation,
				"%s.%s gates the factor AFTER its mutation, which enforces nothing",
				m.server, m.method)
		})
	}
}

// executableSource is the file with every comment removed, so prose can neither
// satisfy nor break a source assertion. A comment is not code; a check that cannot
// tell them apart is satisfied by writing the answer in a comment.
func executableSource(t *testing.T, name string) string {
	t.Helper()
	source, err := os.ReadFile(name)
	require.NoError(t, err)
	fileSet := token.NewFileSet()
	// ParseComments is required: without it File.Comments is EMPTY and this function
	// silently strips nothing, which is a check that looks like it works. It was that
	// way until a mutation test proved a commented-out guard still satisfied a caller.
	parsed, err := parser.ParseFile(fileSet, name, source,
		parser.ParseComments|parser.SkipObjectResolution)
	require.NoError(t, err)

	stripped := append([]byte(nil), source...)
	for _, group := range parsed.Comments {
		// Position().Offset is the canonical byte offset. Deriving one from Pos() and
		// the file's Base() by hand is where this silently became a no-op.
		start := fileSet.Position(group.Pos()).Offset
		end := fileSet.Position(group.End()).Offset
		for i := start; i < end && i < len(stripped); i++ {
			if stripped[i] != '\n' {
				stripped[i] = ' '
			}
		}
	}
	return string(stripped)
}

// The guard against that failure mode: this function must actually remove a comment,
// and must leave code alone. Asserted against a file written for the purpose — asserting
// against this file would compare its own assertion literals, which are code, and a
// needle that appears in both can never be shown absent.
func TestExecutableSourceRemovesComments(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "sample.go")
	require.NoError(t, os.WriteFile(name, []byte(`package sample

// a line comment naming guardedCall()
func f() {
	/* a block comment naming guardedCall() */
	guardedCall()
}
`), 0o600))

	text := executableSource(t, name)
	require.Contains(t, text, "\tguardedCall()", "the executable call must survive")
	require.NotContains(t, text, "a line comment", "a line comment must not survive")
	require.NotContains(t, text, "a block comment", "a block comment must not survive")
	require.Equal(t, 1, strings.Count(text, "guardedCall()"),
		"only the executable occurrence may remain; the two in comments must be gone")
}

type privilegedMutation struct {
	server, method, file string
}

// The privilege-granting and platform-security mutations. Roles, scopes and
// principals grant authority; the platform-admin entries change another identity's
// state or a tenant's ceiling.
//
// UpsertFeatureFlag is deliberately absent: it mutates nothing, returning
// FailedPrecondition for a retired inventory. DisableAgentPrincipal and
// EnableAgentPrincipal are absent for a different reason — they admit on an internal
// service credential, where there is no human actor and so no factor to require.
var privilegedMutationMethods = []privilegedMutation{
	{"PermServer", "CreateRole", "rpcs.go"},
	{"PermServer", "UpdateRole", "rpcs.go"},
	{"PermServer", "DeleteRole", "rpcs.go"},
	{"PermServer", "AssignRole", "rpcs.go"},
	{"PermServer", "RevokeRole", "rpcs.go"},
	{"PermServer", "GrantScope", "rpcs.go"},
	{"PermServer", "RevokeScope", "rpcs.go"},
	{"PlatformAdminServer", "SuspendUser", "rpcs.go"},
	{"PlatformAdminServer", "UnsuspendUser", "rpcs.go"},
	{"PlatformAdminServer", "RevokeSession", "rpcs.go"},
	{"PlatformAdminServer", "OverrideEntitlement", "rpcs.go"},
	{"PrincipalServer", "CreateAgentPrincipal", "principal_rpcs.go"},
	{"PrincipalServer", "RevokePrincipal", "principal_rpcs.go"},
}

type factorCall struct {
	name   string
	invoke func(context.Context) error
}

func privilegedMutations() []factorCall {
	perm := &PermServer{}
	admin := &PlatformAdminServer{}
	return []factorCall{
		{"AssignRole", func(ctx context.Context) error {
			_, err := perm.AssignRole(ctx, &gen.AssignRoleRequest{
				RoleId: factorUUID, SubjectId: factorUUID,
				SubjectKind: gen.SubjectKind_SUBJECT_KIND_USER,
			})
			return err
		}},
		{"RevokeRole", func(ctx context.Context) error {
			_, err := perm.RevokeRole(ctx, &gen.RevokeRoleRequest{
				RoleId: factorUUID, SubjectId: factorUUID,
			})
			return err
		}},
		{"SuspendUser", func(ctx context.Context) error {
			_, err := admin.SuspendUser(ctx, &gen.SuspendUserRequest{
				UserId: factorUUID, Reason: "a recorded reason",
			})
			return err
		}},
		{"OverrideEntitlement", func(ctx context.Context) error {
			_, err := admin.OverrideEntitlement(ctx, &gen.OverrideEntitlementRequest{
				OrgId: factorUUID, Feature: "seats", LimitValue: 10,
				Reason: "a recorded reason",
			})
			return err
		}},
	}
}

func privilegedActorContext() context.Context {
	ctx := stampVerifiedIdentity(context.Background(), mfaActorID, "", auth.Assurance{})
	return auth.WithVerifiedDatabaseIdentity(ctx, mfaActorID, mfaOrgID)
}

const factorUUID = "00000000-0000-4000-8000-0000000000d1"
