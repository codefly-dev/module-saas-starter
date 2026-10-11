package adapters

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// hostAudienceStore answers the one read the host's audience vocabulary is
// derived from. Every other Store method is the embedded nil interface and would
// panic if reached, which is the point: the vocabulary must come from the declared
// registry and from nothing else.
type hostAudienceStore struct {
	business.Store
	bindings []string
	err      error
}

func (s *hostAudienceStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *hostAudienceStore) LiveDeclaredSolutionBindingIDs(context.Context) ([]string, error) {
	return s.bindings, s.err
}

// withHostAudiences installs a service whose vocabulary is exactly these declared
// bindings, and returns the store so a test can make the read fail.
func withHostAudiences(t *testing.T, bindings ...string) *hostAudienceStore {
	t.Helper()
	store := &hostAudienceStore{bindings: bindings}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	previous := service
	service = svc
	t.Cleanup(func() { service = previous })
	return store
}

// Every mint runs one audience rule, and it refuses BY NAME (issue #952).
//
// An audience is what makes two capabilities signed by one key
// non-interchangeable. An empty one was legal on every caller-supplied mint, and
// the verifier could not make up the difference: sdk-go's expectation check
// treats an empty EXPECTED audience as "do not check"
// (workcontext/work_context.go:570), so a verifier site with no value to pass and
// a token with no value to compare agreed vacuously. The host's half of the fix
// is to stamp a value always, which is what makes that sentinel unreachable from
// here.
func TestAMintAudienceIsRequiredAndRefusedByName(t *testing.T) {
	for _, tc := range []struct {
		name     string
		audience string
		code     codes.Code
		says     string
	}{
		{"empty", "", codes.InvalidArgument, "audience is required"},
		{"blank", "   ", codes.InvalidArgument, "audience is required"},
		{"the capability surface itself", ModuleWorkContextAudience, codes.PermissionDenied, ModuleWorkContextAudience},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := requireMintableAudience(tc.audience)
			require.Error(t, err)
			require.Equal(t, tc.code, status.Code(err),
				"the two refusals are different facts and must not share a code")
			require.Contains(t, err.Error(), tc.says,
				"a refusal that does not name what it refused sends an operator to the wrong place")
		})
	}
}

// A whitespace-only audience is not a value. It is the shape a caller lands on by
// templating an empty variable, and treating it as present would stamp a token
// whose audience no consumer can match while reading as "has one".
func TestAnAudienceThatIsOnlyWhitespaceIsNotAnAudience(t *testing.T) {
	require.Error(t, requireMintableAudience("\t\n "))
	require.NoError(t, requireMintableAudience("example-consumer"),
		"an ordinary consumer audience must still pass, or the rule refuses everything")
}

// The closed set is DERIVED from delivered presence, and a mint refuses anything
// outside it BY NAME (issue #952).
//
// Both members of the set are exercised, and the refusals are checked for what
// they say: the whole failure mode this replaces was a free-text audience nobody
// could trace to a consumer, so a refusal that does not name the value is not a
// fix.
func TestTheMintAudienceVocabularyIsClosedAndDerived(t *testing.T) {
	withHostAudiences(t, "acme.test.example", "acme.test.other")
	ctx := context.Background()

	t.Run("a declared binding's audience is in the set", func(t *testing.T) {
		require.NoError(t, requireVocabularyAudience(ctx, business.SolutionAudience("acme.test.example")))
		require.NoError(t, requireVocabularyAudience(ctx, business.SolutionAudience("acme.test.other")))
	})
	t.Run("free text is refused and named", func(t *testing.T) {
		err := requireVocabularyAudience(ctx, "example-consumer")
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Contains(t, err.Error(), `"example-consumer"`)
	})
	t.Run("a binding id without the prefix is not an audience", func(t *testing.T) {
		// The prefix is what keeps a solution audience from colliding with the
		// module one or with a future kind's, so the bare binding id must not pass.
		err := requireVocabularyAudience(ctx, "acme.test.example")
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})
	t.Run("an undeclared binding's audience is refused", func(t *testing.T) {
		err := requireVocabularyAudience(ctx, business.SolutionAudience("acme.test.never-declared"))
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})
	t.Run("the refusal names no binding and no cardinality", func(t *testing.T) {
		// It names the SHAPE and nothing else. Listing the members would report
		// every solution this deployment runs, and COUNTING them reports how many
		// — the same secret at lower resolution, and one a caller can watch change
		// by repeating the probe. TestTheRefusalCarriesNoCardinality holds the
		// count half across set sizes.
		err := requireVocabularyAudience(ctx, "example-consumer")
		require.NotContains(t, err.Error(), "acme.test.example")
		require.NotContains(t, err.Error(), "acme.test.other")
		require.Contains(t, err.Error(), business.SolutionAudiencePrefix+"<binding-id>")
	})
}

// The module capability surface's audience is IN the set and still never
// caller-supplied.
//
// Two rules that would be easy to collapse into one and must not be. It is in the
// vocabulary because it names a real consumer — the surface a module's own
// capability is good for — and it is refused on a caller-supplied mint because a
// context addressed to it is read as a MODULE IDENTITY. Membership and
// mintability are different questions about the same string.
func TestTheModuleAudienceIsInTheSetAndNeverCallerSupplied(t *testing.T) {
	withHostAudiences(t, "acme.test.example")
	set, err := service.HostAudiences(context.Background())
	require.NoError(t, err)
	require.Contains(t, set, business.ModuleCapabilitiesAudience, "the module audience is a member of the vocabulary")

	err = requireVocabularyAudience(context.Background(), ModuleWorkContextAudience)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Contains(t, err.Error(), "module identity",
		"the refusal must say why, or it reads as the audience simply being unknown")
}

// The adapters' constant and the vocabulary's are ONE audience.
//
// Two spellings of one audience is exactly the drift the closed set exists to
// prevent, and it would be invisible: the set would contain one string and the
// mint would refuse the other, so every module mint would fail with "not an
// audience this host serves" while the constant looked right in both files.
func TestTheModuleAudienceConstantsAreOneValue(t *testing.T) {
	require.Equal(t, business.ModuleCapabilitiesAudience, ModuleWorkContextAudience)
}

// A vocabulary that cannot be RESOLVED fails the mint closed, and as Unavailable
// rather than PermissionDenied.
//
// The distinction is the operator's: PermissionDenied sends them to look at a
// grant, Unavailable to look at the registry. A set that cannot be read must read
// as neither "no audience is valid" nor "any is".
func TestAnUnresolvableVocabularyFailsTheMintClosed(t *testing.T) {
	store := withHostAudiences(t, "acme.test.example")
	store.err = context.DeadlineExceeded
	err := requireVocabularyAudience(context.Background(), business.SolutionAudience("acme.test.example"))
	require.Equal(t, codes.Unavailable, status.Code(err),
		"an audience the set WOULD contain must still be refused when the set cannot be read")
}

// NO HOST SITE EVER PASSES AN EMPTY AUDIENCE — the test the ruling asked for, and
// the reason sdk-go's `""`-means-skip sentinel is safe to leave for the cutover.
//
// The sentinel is `if check.want != "" && check.got != check.want` in
// sdk-go's workcontext/work_context.go: an empty EXPECTED audience means "do not
// check". That is not this repo's to delete. What this repo can guarantee is that
// the expectation is never empty, which makes the sentinel unreachable from here.
//
// Asserted over the SET, not over one call: every member of the host's vocabulary
// is non-empty, and every mint path refuses an empty audience before it reaches a
// signer. A mutation that lets one through fails here.
func TestNoHostAudienceIsEverEmpty(t *testing.T) {
	withHostAudiences(t, "acme.test.example", "", "   ")
	set, err := service.HostAudiences(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, set)
	for audience := range set {
		require.NotEmpty(t, audience,
			"an empty member would make sdk-go's empty-expectation sentinel reachable: an empty expected audience is not checked at all")
		require.Equal(t, audience, string([]rune(audience)), "members are compared by value, so they must be exact")
	}
	// A blank binding id contributes NO audience rather than the bare prefix: a
	// "solution:" with nothing after it would be a member that names no binding.
	require.NotContains(t, set, business.SolutionAudiencePrefix)
	require.Len(t, set, 2, "one module audience and one real binding; the blank and whitespace ids contribute nothing")

	// And the structural half refuses an empty audience without consulting the set
	// at all, so it holds even when the registry cannot be read.
	require.Error(t, requireMintableAudience(""))
	require.Error(t, requireMintableAudience("   "))
}

// countingAudienceStore records how many control-plane reads the vocabulary makes,
// which is the only way to prove the mint is off the hot path: a passing mint says
// nothing about how many transactions it opened.
type countingAudienceStore struct {
	hostAudienceStore
	reads int
}

func (s *countingAudienceStore) LiveDeclaredSolutionBindingIDs(context.Context) ([]string, error) {
	s.reads++
	return s.bindings, s.err
}

func withCountingHostAudiences(t *testing.T, bindings ...string) *countingAudienceStore {
	t.Helper()
	store := &countingAudienceStore{hostAudienceStore: hostAudienceStore{bindings: bindings}}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	previous := service
	service = svc
	t.Cleanup(func() { service = previous })
	return store
}

// The vocabulary is read from a BOUNDED SNAPSHOT, not on every call.
//
// It used to open a control-plane transaction per call — twice per mint, since the
// mint checks the audience and then narrows the actor's ceiling — on a pool whose
// MaxConns is the pgx default and which every other control-plane capability
// shares, the delivery reconciler among them. A mint storm therefore queued
// delivery: the mechanism that withdraws a compromised solution.
//
// Asserted on the READ COUNT, because that is the defect. A latency assertion
// would be flaky and a passing mint proves nothing about transactions opened.
func TestTheAudienceVocabularyIsReadFromABoundedSnapshot(t *testing.T) {
	store := withCountingHostAudiences(t, "acme.test.example")
	ctx := context.Background()
	audience := business.SolutionAudience("acme.test.example")

	require.NoError(t, requireVocabularyAudience(ctx, audience))
	require.Equal(t, 1, store.reads, "the first resolution reads")

	for range 50 {
		require.NoError(t, requireVocabularyAudience(ctx, audience))
		_, err := service.LiveHostAudiences(ctx, []string{audience})
		require.NoError(t, err)
	}
	require.Equal(t, 1, store.reads,
		"a hundred further resolutions inside the bound must open no further control-plane transaction")
}

// The bound EXPIRES, so another replica's withdrawal reaches this one.
//
// Proved by moving the clock rather than by sleeping it out: a bound no test can
// move is a bound asserted by its constant instead of by its behaviour.
func TestTheAudienceSnapshotExpiresAtTheBound(t *testing.T) {
	store := withCountingHostAudiences(t, "acme.test.example")
	now := time.Now()
	service.SetAudienceClockForTest(func() time.Time { return now })
	ctx := context.Background()
	audience := business.SolutionAudience("acme.test.example")

	require.NoError(t, requireVocabularyAudience(ctx, audience))
	require.Equal(t, 1, store.reads)

	now = now.Add(business.HostAudienceCacheBound - time.Second)
	require.NoError(t, requireVocabularyAudience(ctx, audience))
	require.Equal(t, 1, store.reads, "inside the bound the snapshot still serves")

	now = now.Add(2 * time.Second)
	require.NoError(t, requireVocabularyAudience(ctx, audience))
	require.Equal(t, 2, store.reads, "past the bound it re-reads")

	// And the re-read is what carries another replica's withdrawal: the same
	// audience is refused once the registry no longer holds the binding.
	store.bindings = nil
	now = now.Add(business.HostAudienceCacheBound + time.Second)
	err := requireVocabularyAudience(ctx, audience)
	require.Equal(t, codes.PermissionDenied, status.Code(err),
		"a withdrawal another replica applied must reach this one at the bound")
}

// A FAILED read does not install an empty snapshot.
//
// This is the direction that would be catastrophic and quiet: an outage during a
// refresh installing "this host serves no solutions" would refuse every solution
// mint for a whole bound, and look exactly like a vocabulary decision.
func TestAFailedVocabularyReadDoesNotPoisonTheSnapshot(t *testing.T) {
	store := withCountingHostAudiences(t, "acme.test.example")
	now := time.Now()
	service.SetAudienceClockForTest(func() time.Time { return now })
	ctx := context.Background()
	audience := business.SolutionAudience("acme.test.example")
	require.NoError(t, requireVocabularyAudience(ctx, audience))

	now = now.Add(business.HostAudienceCacheBound + time.Second)
	store.err = context.DeadlineExceeded
	require.Equal(t, codes.Unavailable, status.Code(requireVocabularyAudience(ctx, audience)),
		"the refresh failed, so the mint fails closed")

	store.err = nil
	require.NoError(t, requireVocabularyAudience(ctx, audience),
		"and once the read recovers the audience is served again, not refused from a poisoned snapshot")
}

// Invalidation makes THIS replica's withdrawal visible to the very next mint,
// without waiting out the bound.
func TestInvalidationMakesAWithdrawalVisibleAtOnce(t *testing.T) {
	store := withCountingHostAudiences(t, "acme.test.example")
	now := time.Now()
	service.SetAudienceClockForTest(func() time.Time { return now })
	ctx := context.Background()
	audience := business.SolutionAudience("acme.test.example")
	require.NoError(t, requireVocabularyAudience(ctx, audience))

	store.bindings = nil
	require.NoError(t, requireVocabularyAudience(ctx, audience),
		"without invalidation the snapshot still serves inside the bound")

	service.InvalidateHostAudiences()
	require.Equal(t, codes.PermissionDenied, status.Code(requireVocabularyAudience(ctx, audience)),
		"the apply path invalidates, so the next mint sees the withdrawal")
}

// The refusal is a CONSTANT shape and carries no cardinality.
//
// An earlier version counted the members on the reasoning that counting is not
// listing. A count is information about the same secret: one refused mint told any
// caller how many solutions this deployment runs, and repeating the probe told them
// when a solution was installed or withdrawn.
func TestTheRefusalCarriesNoCardinality(t *testing.T) {
	ctx := context.Background()
	for _, bindings := range [][]string{
		{"acme.test.one"},
		{"acme.test.one", "acme.test.two", "acme.test.three", "acme.test.four"},
	} {
		withHostAudiences(t, bindings...)
		err := requireVocabularyAudience(ctx, "nothing-serves-this")
		require.Error(t, err)
		require.NotRegexp(t, `[0-9]`, err.Error(),
			"a digit in the refusal is a cardinality channel: %s", err.Error())
		require.Contains(t, err.Error(), business.SolutionAudiencePrefix+"<binding-id>",
			"it names the SHAPE instead")
	}
}
