package adapters

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codefly-dev/core/workcontext"
	sdk "github.com/codefly-dev/sdk-go/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// A composed module verifies a capability presented to it AT FULL STRENGTH,
// against the live authority this host serves (issue #1107).
//
// These tests are the callee's side of the seal. They build the verifier a
// composed module builds — sdk-go/workcontext's PinnedVerifier, which is core's
// Verifier with its configuration settled — back its seal, revision and grant
// sources with the served RPC, and then move the issuer's live state under a
// capability that is already signed, in each way a revocation reaches one: the
// installation stops being held, an epoch advances, the tenant's authorization
// revision advances, the owner stops being a member, an approved build is
// replaced, a binding is withdrawn.
//
// Each must be REFUSED, because the alternative is what a module had before:
// a forwarding hop's inspection, which accepts all of them until the capability
// expires.
//
// Nothing here fakes the surface under test. The verifier is the published one,
// the answers come out of the real RPC handler — authentication, live grant
// re-read, capability inspection and all — and the sources are as narrow as
// core's interfaces, so a test that passes proves the wire carries what a
// verifier needs.

const (
	authorityOrg          = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	authorityPerson       = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	authorityInstallation = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	authorityStranger     = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	authorityOtherOrg     = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"

	// Two declared modules, each a possible callee. documents is the one the
	// capability below is addressed to; reports is the one that must not be
	// able to resolve the authority behind it.
	authorityPrincipals = `{"documents":{"workload":{"service_account":"documents","namespace":"acme-prod","container":"app"},"tenant":"` +
		authorityOrg + `"},` +
		`"reports":{"workload":{"service_account":"reports","namespace":"acme-prod","container":"app"},"tenant":"` +
		authorityOrg + `"}}`
)

// servedSources is the composed module's side: core's three sources, each one
// RPC call to the host's module authority endpoint, each carrying the capability
// being verified.
//
// It is as narrow as the interfaces it implements, and that is the point of
// those interfaces being narrow. It makes no decision — every answer is
// translated, never interpreted — and it refuses anything it does not
// understand: an answer on another arm than the question, an answer about
// another principal, installation or binding, or a state the host did not set.
// A client that read an unexpected answer as a permissive one would turn the
// strongest check in the model into the easiest to defeat.
//
// `owner` is bound at construction, exactly as this host binds it on its own
// revision source: core's RevisionSource is keyed by tenant alone, and this
// host's revision is the greater of the organization's and the owner's own.
type servedSources struct {
	t          *testing.T
	token      string
	owner      string
	credential string
}

func (s servedSources) ask(
	ctx context.Context, question any,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
	s.t.Helper()
	request := &gen.ModuleResolveWorkContextAuthorityRequest{WorkContext: s.token}
	switch asked := question.(type) {
	case *gen.WorkContextSealQuestion:
		request.Question = &gen.ModuleResolveWorkContextAuthorityRequest_Seal{Seal: asked}
	case *gen.WorkContextPrincipalEpochQuestion:
		request.Question = &gen.ModuleResolveWorkContextAuthorityRequest_PrincipalEpoch{PrincipalEpoch: asked}
	case *gen.WorkContextApprovedBuildQuestion:
		request.Question = &gen.ModuleResolveWorkContextAuthorityRequest_ApprovedBuild{ApprovedBuild: asked}
	case *gen.WorkContextOperationBindingQuestion:
		request.Question = &gen.ModuleResolveWorkContextAuthorityRequest_OperationBinding{OperationBinding: asked}
	case *gen.WorkContextAuthorizationRevisionQuestion:
		request.Question = &gen.ModuleResolveWorkContextAuthorityRequest_AuthorizationRevision{AuthorizationRevision: asked}
	case *gen.WorkContextApprovalGrantQuestion:
		request.Question = &gen.ModuleResolveWorkContextAuthorityRequest_ApprovalGrant{ApprovalGrant: asked}
	default:
		s.t.Fatalf("unknown question %T", question)
	}
	// The module's own credential, held by the client and put on the call it
	// makes — never read off the context core handed it, which carries a
	// deadline and nothing else.
	credentialed := metadata.NewIncomingContext(ctx,
		metadata.Pairs(workContextHeader, s.credential))
	return ModuleCapabilitiesSingleton().ResolveWorkContextAuthority(credentialed, request)
}

func (s servedSources) Seal(ctx context.Context, principalID, installationID string) (workcontext.Seal, error) {
	response, err := s.ask(ctx, &gen.WorkContextSealQuestion{
		PrincipalId: principalID, InstallationId: installationID,
	})
	if err != nil {
		return workcontext.Seal{}, err
	}
	answer := response.GetSeal()
	if answer == nil {
		return workcontext.Seal{}, fmt.Errorf("the host answered no seal")
	}
	if answer.GetInstallationId() != installationID {
		return workcontext.Seal{}, fmt.Errorf("the host answered for installation %q, not %q",
			answer.GetInstallationId(), installationID)
	}
	switch answer.GetState() {
	case gen.WorkContextSealState_WORK_CONTEXT_SEAL_STATE_HELD:
		return workcontext.Seal{
			InstallationID:       answer.GetInstallationId(),
			InstallationRevision: answer.GetInstallationRevision(),
		}, nil
	case gen.WorkContextSealState_WORK_CONTEXT_SEAL_STATE_UNHELD:
		return workcontext.Seal{}, workcontext.ErrNoSeal
	default:
		return workcontext.Seal{}, fmt.Errorf("the host answered no seal state")
	}
}

func (s servedSources) PrincipalEpoch(ctx context.Context, principalID string) (uint64, error) {
	response, err := s.ask(ctx, &gen.WorkContextPrincipalEpochQuestion{PrincipalId: principalID})
	if err != nil {
		return 0, err
	}
	answer := response.GetPrincipalEpoch()
	if answer == nil || answer.GetPrincipalId() != principalID {
		return 0, fmt.Errorf("the host answered no epoch for principal %q", principalID)
	}
	switch answer.GetState() {
	case gen.WorkContextPrincipalState_WORK_CONTEXT_PRINCIPAL_STATE_KNOWN:
		return answer.GetEpoch(), nil
	case gen.WorkContextPrincipalState_WORK_CONTEXT_PRINCIPAL_STATE_UNKNOWN:
		return 0, workcontext.ErrNoSeal
	default:
		return 0, fmt.Errorf("the host answered no principal state")
	}
}

func (s servedSources) OperationBinding(ctx context.Context, bindingID string) (workcontext.OperationBinding, error) {
	response, err := s.ask(ctx, &gen.WorkContextOperationBindingQuestion{BindingId: bindingID})
	if err != nil {
		return workcontext.OperationBinding{}, err
	}
	answer := response.GetOperationBinding()
	if answer == nil || answer.GetBindingId() != bindingID {
		return workcontext.OperationBinding{}, fmt.Errorf("the host answered no binding %q", bindingID)
	}
	switch answer.GetState() {
	case gen.WorkContextOperationBindingState_WORK_CONTEXT_OPERATION_BINDING_STATE_RESOLVED:
		return workcontext.OperationBinding{
			ID:             answer.GetBindingId(),
			PrincipalID:    answer.GetPrincipalId(),
			InstallationID: answer.GetInstallationId(),
			Revision:       answer.GetRevision(),
			Incarnation:    answer.GetIncarnation(),
			Revoked:        answer.GetRevoked(),
		}, nil
	case gen.WorkContextOperationBindingState_WORK_CONTEXT_OPERATION_BINDING_STATE_ABSENT:
		return workcontext.OperationBinding{}, workcontext.ErrNoBinding
	default:
		return workcontext.OperationBinding{}, fmt.Errorf("the host answered no binding state")
	}
}

func (s servedSources) AuthorizationRevision(_ context.Context, _ string) (uint64, error) {
	response, err := s.ask(context.Background(),
		&gen.WorkContextAuthorizationRevisionQuestion{OwnerPrincipalId: s.owner})
	if err != nil {
		return 0, err
	}
	answer := response.GetAuthorizationRevision()
	if answer == nil || answer.GetOwnerPrincipalId() != s.owner {
		return 0, fmt.Errorf("the host answered no authorization revision for owner %q", s.owner)
	}
	return answer.GetRevision(), nil
}

func (s servedSources) Grant(ctx context.Context, grantID string) (*workcontext.Grant, error) {
	response, err := s.ask(ctx, &gen.WorkContextApprovalGrantQuestion{GrantId: grantID})
	if err != nil {
		return nil, err
	}
	answer := response.GetApprovalGrant()
	if answer == nil || answer.GetGrantId() != grantID {
		return nil, fmt.Errorf("the host answered no grant %q", grantID)
	}
	// NOT_RECORDED is an answer and still a refusal: core has no "absent grant"
	// sentinel, because a capability claiming an approval the issuer never made
	// is not a credential.
	return nil, fmt.Errorf("the issuer records no approval grant %q (state %s)", grantID, answer.GetState())
}

var (
	_ workcontext.SealSource     = servedSources{}
	_ workcontext.RevisionSource = servedSources{}
	_ workcontext.GrantSource    = servedSources{}
)

// inactiveModuleAuthority is a live-authority read reporting that the caller's
// module is installed nowhere any more — the narrowing or uninstall every
// capability path on this surface has to re-read.
type inactiveModuleAuthority struct{}

func (inactiveModuleAuthority) LiveModuleAuthority(
	context.Context, string, string, string,
) (*business.LiveModuleAuthority, error) {
	return nil, business.ErrModuleInstallationInactive
}

func (inactiveModuleAuthority) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

// narrowedModuleAuthority answers a live authority whose installation and
// counters still MATCH the caller's credential — so the credential itself
// verifies — but whose scope no longer carries the capability surface. It is the
// narrowing only the grant re-read can catch.
type narrowedModuleAuthority struct{ service *business.Service }

func (f narrowedModuleAuthority) LiveModuleAuthority(
	_ context.Context, principal, _, _ string,
) (*business.LiveModuleAuthority, error) {
	grant := f.service.ModulePrincipals()[principal]
	scope := business.FullInstallationScope(grant)
	scope.Capabilities = false
	return &business.LiveModuleAuthority{
		InstallationID: "installation-current", InstallationRevision: 1, ProducerEpoch: 1,
		RegistryBindingID: grant.PresenceBinding, Scope: scope,
	}, nil
}

func (f narrowedModuleAuthority) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

// localRun is the transport posture a module's key-set admission reads. The key
// set here is served over loopback, which sdk-go admits on a local run and
// refuses in a deployment — so this is the test's own posture and never a
// relaxation of the rule.
type localRun struct{}

func (localRun) WorkspaceValueIfSet(string, string) (string, bool, error) { return "", false, nil }
func (localRun) IsLocalRun() bool                                         { return true }

// serveKeySet publishes the issuer's verification key the way a module fetches
// it: a JWKS document over HTTP, decoded by the CONSUMER. The decoding is the
// consumer's because a key set's encoding is the issuer's format, and sdk-go may
// not name a signature primitive.
func serveKeySet(t *testing.T) sdk.KeySet {
	t.Helper()
	document, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kid": workContextSingleton.keyID,
		"kty": "OKP",
		"crv": "Ed25519",
		"x":   base64.RawURLEncoding.EncodeToString(workContextSingleton.publicKey),
	}}})
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(document)
	}))
	t.Cleanup(server.Close)

	endpoint, err := sdk.ResolveKeySetEndpoint(server.URL+"/v1/auth/.well-known/jwks.json", localRun{})
	require.NoError(t, err)
	keys, err := sdk.AcquireKeySet(context.Background(), endpoint, func(payload []byte) (map[string][]byte, error) {
		var document struct {
			Keys []struct {
				Kid string `json:"kid"`
				X   string `json:"x"`
			} `json:"keys"`
		}
		if err := json.Unmarshal(payload, &document); err != nil {
			return nil, err
		}
		decoded := make(map[string][]byte, len(document.Keys))
		for _, key := range document.Keys {
			material, err := base64.RawURLEncoding.DecodeString(key.X)
			if err != nil {
				return nil, err
			}
			decoded[key.Kid] = material
		}
		return decoded, nil
	})
	require.NoError(t, err)
	return keys
}

// verifierOver is the verifier a composed module builds: issuer and audience
// pinned, keys from an admitted endpoint, every source backed by the host, and a
// replay store of its own — single-use consumption is the consumer's, never the
// issuer's to do on its behalf, and a deployment with more than one replica
// needs that store to be durable and shared.
func verifierOver(t *testing.T, audience string, sources servedSources) *sdk.PinnedVerifier {
	t.Helper()
	verifier, err := sdk.NewVerifier(sdk.VerifierSettings{
		Issuer:    "accounts.test",
		Audience:  audience,
		Revisions: sources,
		Replay:    &workcontext.MemoryReplayStore{},
		Grants:    sources,
		Seals:     sources,
	}, serveKeySet(t))
	require.NoError(t, err)
	return verifier
}

// authorityFixture is one host with two declared modules installed, a person's
// capability addressed to one of them, and both modules' own credentials.
type authorityFixture struct {
	answers *sealAnswersFake
	token   string
	owner   string
	caller  string
	other   string
}

func installWorkContextAuthorityFixture(t *testing.T) *authorityFixture {
	t.Helper()
	installModuleWorkContextService(t, authorityPrincipals)
	installModuleWorkContextAuthority(t)
	answers, ok := workContextSingleton.seals.(*sealAnswersFake)
	require.True(t, ok)

	token, _, err := tryMintTestCapability(workContextSingleton, testCapability{
		Audience:     "documents",
		Tenant:       authorityOrg,
		Owner:        authorityPerson,
		Installation: authorityInstallation,
	})
	require.NoError(t, err)

	return &authorityFixture{
		answers: answers,
		token:   token,
		owner:   authorityPerson,
		caller:  mintModuleIdentity(t, business.ModulePrincipalID("documents"), authorityOrg),
		other:   mintModuleIdentity(t, business.ModulePrincipalID("reports"), authorityOrg),
	}
}

// sources is the client half, for an assertion about one answer rather than
// about a whole verification.
func (f *authorityFixture) sources(t *testing.T, credential string) servedSources {
	t.Helper()
	return servedSources{t: t, token: f.token, owner: f.owner, credential: credential}
}

func (f *authorityFixture) verifier(t *testing.T, credential string) *sdk.PinnedVerifier {
	t.Helper()
	return verifierOver(t, "documents", f.sources(t, credential))
}

// The headline: a module verifies a host-minted capability at full strength
// against the served sources. Every check core makes beyond a hop's inspection —
// the authorization revision, the sealed installation and its revision, the
// owner's epoch, the execution — is answered over the wire here.
func TestAComposedModuleVerifiesAHostCapabilityAgainstTheServedSources(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)

	verified, err := fixture.verifier(t, fixture.caller).Verify(context.Background(), fixture.token)

	require.NoError(t, err)
	require.Equal(t, authorityOrg, verified.Context().GetTenantId())
	require.Equal(t, authorityPerson, verified.Context().GetOwnerPrincipalId())
	require.Equal(t, authorityInstallation, verified.Context().GetSeal().GetInstallationId())
	// Every answer was asked for the capability's own tenant, which is the
	// caller's. A serving path that dropped the tenant would answer another
	// organization's state for these same identifiers.
	asked := fixture.answers.askedTenants()
	require.NotEmpty(t, asked)
	for _, tenant := range asked {
		require.Equal(t, authorityOrg, tenant)
	}
}

// A revoked installation: the capability is signed, unexpired and addressed
// here, and the organization no longer holds the installation it is sealed to.
func TestAnUnheldInstallationIsRefusedThroughTheServedSources(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	verifier := fixture.verifier(t, fixture.caller)
	require.NoError(t, mustVerify(t, verifier, fixture.token))
	sources := fixture.sources(t, fixture.caller)

	fixture.answers.unheld[authorityInstallation] = true

	// The source answers UNHELD, asserted on its own: a capability sealed at a
	// revision the answer does not carry would be refused by the revision
	// comparison too, so an end-to-end refusal alone would pass for an answer
	// that said "held, at revision zero".
	_, err := sources.Seal(context.Background(), authorityPerson, authorityInstallation)
	require.ErrorIs(t, err, workcontext.ErrNoSeal)

	_, err = verifier.Verify(context.Background(), fixture.token)
	require.ErrorIs(t, err, workcontext.ErrRevoked)
}

// A stale epoch: the owner's authority changed, which advances its epoch, and
// every capability sealed to the previous one stops verifying.
func TestAStalePrincipalEpochIsRefusedThroughTheServedSources(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	verifier := fixture.verifier(t, fixture.caller)
	require.NoError(t, mustVerify(t, verifier, fixture.token))
	sources := fixture.sources(t, fixture.caller)

	fixture.answers.epochs[authorityPerson] = 2

	epoch, err := sources.PrincipalEpoch(context.Background(), authorityPerson)
	require.NoError(t, err)
	require.Equal(t, uint64(2), epoch, "the served epoch is the one that moved")

	_, err = verifier.Verify(context.Background(), fixture.token)
	require.ErrorIs(t, err, workcontext.ErrRevoked)
}

// A stale authorization revision: the coarse lever, which cuts off every
// capability of the tenant minted before it moved.
func TestAStaleAuthorizationRevisionIsRefusedThroughTheServedSources(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	verifier := fixture.verifier(t, fixture.caller)
	require.NoError(t, mustVerify(t, verifier, fixture.token))
	sources := fixture.sources(t, fixture.caller)

	fixture.answers.setRevision(13)

	revision, err := sources.AuthorizationRevision(context.Background(), authorityOrg)
	require.NoError(t, err)
	require.Equal(t, uint64(13), revision, "the served revision is the one that moved")

	_, err = verifier.Verify(context.Background(), fixture.token)
	require.ErrorIs(t, err, workcontext.ErrRevoked)
}

// THE REVISION IS ANSWERED FOR THE CAPABILITY'S OWNER, not for the tenant alone.
//
// Core's RevisionSource is keyed by tenant and this host's revision is the
// greater of the organization's and the owner's own, so an answer that ignored
// the owner would be a different number from the one the mint stamped — and a
// capability would be refused, or accepted, on somebody else's counter.
func TestTheServedRevisionIsTheCapabilityOwnersOwn(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	fixture.answers.revisions[authorityPerson] = 12
	fixture.answers.setRevision(99)

	revision, err := fixture.sources(t, fixture.caller).
		AuthorizationRevision(context.Background(), authorityOrg)

	require.NoError(t, err)
	require.Equal(t, uint64(12), revision, "the owner's own revision, not the organization's")
	require.NoError(t, mustVerify(t, fixture.verifier(t, fixture.caller), fixture.token))
}

// A REMOVED MEMBER IS A DECISION, NOT AN OUTAGE — and it is the first question
// core asks, so nothing downstream can catch it.
//
// The row-backed revision read answers "this principal has no authorization
// record in this organization" for a person whose membership is gone. That is a
// verdict: the capability is revoked and its holder must mint again. Answering
// UNAVAILABLE instead — which an earlier revision of this surface did for every
// plain error — tells a callee "nothing was decided", so a retry policy retries
// it forever while a cache of the last good verdict keeps admitting the removed
// person.
func TestARemovedOwnerIsRefusedAsADecisionNotAnOutage(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	require.NoError(t, mustVerify(t, fixture.verifier(t, fixture.caller), fixture.token))

	fixture.answers.refuseRevision(authorityPerson)

	_, err := fixture.sources(t, fixture.caller).AuthorizationRevision(context.Background(), authorityOrg)
	require.Equal(t, codes.FailedPrecondition, status.Code(err),
		"a removed member is a refusal the caller must not retry")

	_, err = fixture.verifier(t, fixture.caller).Verify(context.Background(), fixture.token)
	require.Error(t, err, "and the capability does not verify")
}

// A lookup that FAILED is the other answer, and it must stay distinguishable
// from the decision above or the two become one.
func TestAFailedLookupIsUnavailableNotARefusal(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	fixture.answers.revisionErr[authorityPerson] = fmt.Errorf("the database is unreachable")

	_, err := fixture.sources(t, fixture.caller).AuthorizationRevision(context.Background(), authorityOrg)

	require.Equal(t, codes.Unavailable, status.Code(err))
	require.NotContains(t, err.Error(), "database is unreachable",
		"the cause is logged, never returned: these answers are about a third party")
}

// A principal the organization has no authorization record for is refused, and
// it is refused through BOTH questions that could have let it through.
//
// The epoch question answers UNKNOWN, which a verifier turns into a refusal —
// never an epoch of zero, which compares equal to a real epoch of zero and so
// would pass for exactly the capabilities that claim nothing. The approved-build
// question refuses outright rather than answering "bears no execution", which
// would mint an unbound capability for an identity the host has never heard of.
func TestAnUnknownPrincipalIsRefusedThroughTheServedSources(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	verifier := fixture.verifier(t, fixture.caller)
	require.NoError(t, mustVerify(t, verifier, fixture.token))
	sources := fixture.sources(t, fixture.caller)

	fixture.answers.unknown[authorityPerson] = true

	_, err := sources.PrincipalEpoch(context.Background(), authorityPerson)
	require.ErrorIs(t, err, workcontext.ErrNoSeal)

	_, err = verifier.Verify(context.Background(), fixture.token)
	require.Error(t, err)
}

// THE CAPABILITY IS THE AUTHORIZATION. A module may resolve the live authority
// behind capabilities addressed to IT, and behind no others — and presenting the
// capability is what makes that a property rather than a label on a request.
//
// reports is a declared, installed, running module with a valid credential of
// its own. Presenting documents' capability, it is refused: the host inspects
// the capability against the names the CALLER holds, so there is nothing for
// reports to assert about what it is asking.
func TestAModuleCannotResolveAuthorityForACapabilityAddressedElsewhere(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)

	foreign := fixture.sources(t, fixture.other)
	_, err := foreign.Seal(context.Background(), authorityPerson, authorityInstallation)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// No answer leaks through another question either.
	_, err = foreign.PrincipalEpoch(context.Background(), authorityPerson)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = foreign.AuthorizationRevision(context.Background(), authorityOrg)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// And the whole verification fails rather than degrading: a module that
	// cannot resolve the live state refuses the capability.
	_, err = verifierOver(t, "documents", foreign).Verify(context.Background(), fixture.token)
	require.Error(t, err)
}

// A capability addressed to this module IN ANOTHER ORGANIZATION resolves
// nothing. The audience alone does not bound the tenant: a module installed in
// several organizations is addressed by the same name in each, and every answer
// here is per organization. Serving one tenant's state for another's capability
// would compare the capability's sealed values against counters that belong to
// a different organization — which can refuse a good capability as easily as it
// can admit a revoked one.
//
// A module that legitimately acts in several asks with the credential it minted
// for the organization in question.
func TestACapabilityInAnotherOrganizationResolvesNothing(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	elsewhere, _, err := tryMintTestCapability(workContextSingleton, testCapability{
		Audience:     "documents",
		Tenant:       authorityOtherOrg,
		Owner:        authorityPerson,
		Installation: authorityInstallation,
	})
	require.NoError(t, err)
	foreign := servedSources{
		t: t, token: elsewhere, owner: authorityPerson, credential: fixture.caller,
	}

	_, err = foreign.PrincipalEpoch(context.Background(), authorityPerson)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	_, err = foreign.Seal(context.Background(), authorityPerson, authorityInstallation)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// A question must be about the capability presented with it. A client asking
// about a principal, installation, binding or grant the capability does not name
// is told so — never quietly answered about the capability's own values, which
// would make a client bug look like a working check.
func TestAQuestionMustBeAboutThePresentedCapability(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	sources := fixture.sources(t, fixture.caller)

	// A principal that is neither the owner nor a hop: the membership oracle
	// this surface must not be.
	_, err := sources.PrincipalEpoch(context.Background(), authorityStranger)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	// Another installation, and another owner's revision.
	_, err = sources.Seal(context.Background(), authorityPerson, authorityStranger)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	stranger := servedSources{t: t, token: fixture.token, owner: authorityStranger, credential: fixture.caller}
	_, err = stranger.AuthorizationRevision(context.Background(), authorityOrg)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	// A binding and a grant this capability does not carry. This host mints no
	// grant hop, so the grant question is unaddressable for every capability it
	// issues — which is why NOT_RECORDED is unreachable through this surface
	// rather than merely unused.
	_, err = sources.OperationBinding(context.Background(), "source-delegation/guessed/documents")
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = sources.Grant(context.Background(), "an-approval-nobody-made")
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// The caller's live grant is re-read on every question, like every other path on
// this surface: a module whose installation is no longer active resolves nothing,
// at its next question rather than at its credential's expiry.
func TestAWithdrawnCallerInstallationStopsResolvingAuthority(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	sources := fixture.sources(t, fixture.caller)
	_, err := sources.PrincipalEpoch(context.Background(), authorityPerson)
	require.NoError(t, err)

	service.SetModuleAuthorityReads(inactiveModuleAuthority{}, nil)

	// Unauthenticated, and from one step further back than the grant read: the
	// caller's OWN credential is sealed to an installation the host no longer
	// holds, so verifying it already fails. That is the refusal this surface
	// wants — it never reaches a source — and it is a decision, not an outage.
	_, err = sources.PrincipalEpoch(context.Background(), authorityPerson)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

// A NARROWED caller resolves nothing either, and the installation it is sealed
// to does not move: the capability surface is taken out of its installed scope
// while every counter the credential carries stays as it was.
//
// The refusal lands at Unauthenticated rather than PermissionDenied, and where
// it lands is worth recording. A module principal's authorization revision IS
// its installed grant, so verifying the caller's own credential already reads
// that grant — the narrowing stops the credential before the surface's own
// grant read is reached. The live re-read in the authorization path is
// therefore the second of two, not the only one, and this test pins the
// behaviour rather than the layer.
func TestANarrowedCallerGrantStopsResolvingAuthority(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	sources := fixture.sources(t, fixture.caller)
	_, err := sources.PrincipalEpoch(context.Background(), authorityPerson)
	require.NoError(t, err)

	service.SetModuleAuthorityReads(narrowedModuleAuthority{service: service}, nil)

	_, err = sources.PrincipalEpoch(context.Background(), authorityPerson)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

// The identity comes from the signed capability, never from the request, so a
// caller that reaches this listener with no credential resolves nothing.
func TestResolvingAuthorityWithoutAModuleCredentialIsUnauthenticated(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)

	_, err := ModuleCapabilitiesSingleton().ResolveWorkContextAuthority(context.Background(),
		&gen.ModuleResolveWorkContextAuthorityRequest{
			WorkContext: fixture.token,
			Question: &gen.ModuleResolveWorkContextAuthorityRequest_PrincipalEpoch{
				PrincipalEpoch: &gen.WorkContextPrincipalEpochQuestion{PrincipalId: authorityPerson},
			},
		})

	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

// A request that asks nothing, or asks about nothing, is refused by the schema.
// The oneof is required because an empty question has no safe default, and the
// capability is required because it is the whole of the authorization.
func TestResolvingAuthorityNeedsAQuestionAndACapability(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(workContextHeader, fixture.caller))

	_, err := ModuleCapabilitiesSingleton().ResolveWorkContextAuthority(ctx,
		&gen.ModuleResolveWorkContextAuthorityRequest{WorkContext: fixture.token})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = ModuleCapabilitiesSingleton().ResolveWorkContextAuthority(ctx,
		&gen.ModuleResolveWorkContextAuthorityRequest{
			Question: &gen.ModuleResolveWorkContextAuthorityRequest_PrincipalEpoch{
				PrincipalEpoch: &gen.WorkContextPrincipalEpochQuestion{PrincipalId: authorityPerson},
			},
		})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// A capability this issuer did not sign is refused, whatever it claims. The host
// authenticates what it is asked about: that is what makes every identifier it
// then reads off the capability the issuer's own statement rather than a
// caller's.
func TestAForeignCapabilityResolvesNothing(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	forged := servedSources{
		t: t, token: fixture.token + "x", owner: authorityPerson, credential: fixture.caller,
	}

	_, err := forged.PrincipalEpoch(context.Background(), authorityPerson)

	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// The served answers are the ISSUER'S OWN. Not an equivalent read, not a second
// query that happens to agree today: the same sources, so a mint and a callee's
// verdict cannot disagree about what "current" means.
//
// This is the property the whole surface rests on, and it is the one a
// refactoring breaks silently — a serving path that grew its own installation
// read would pass every test above and still drift from issuance.
func TestTheServedAnswersAreTheIssuersOwnSources(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	sources := fixture.sources(t, fixture.caller)
	issuerSeals, issuerRevisions, _ := workContextSingleton.moduleAuthoritySources(authorityOrg, fixture.owner)

	served, err := sources.Seal(context.Background(), authorityPerson, authorityInstallation)
	require.NoError(t, err)
	direct, err := issuerSeals.Seal(context.Background(), authorityPerson, authorityInstallation)
	require.NoError(t, err)
	require.Equal(t, direct, served)

	servedEpoch, err := sources.PrincipalEpoch(context.Background(), authorityPerson)
	require.NoError(t, err)
	directEpoch, err := issuerSeals.PrincipalEpoch(context.Background(), authorityPerson)
	require.NoError(t, err)
	require.Equal(t, directEpoch, servedEpoch)

	// And the move is visible through both at once, so the equality above is not
	// two constants agreeing.
	fixture.answers.setRevision(21)
	servedRevision, err := sources.AuthorizationRevision(context.Background(), authorityOrg)
	require.NoError(t, err)
	directRevision, err := issuerRevisions.AuthorizationRevision(context.Background(), authorityOrg)
	require.NoError(t, err)
	require.Equal(t, directRevision, servedRevision)
	require.Equal(t, uint64(21), servedRevision)
}

// delegationSources is a module's own execution and the binding it exercises,
// over the delegation fixture: a capability owned by the person who connected a
// source, carrying the using module's hop, sealed to that module's installation
// and to the delegation as its operation binding. reports is its audience, so
// reports is the callee that verifies it.
func delegationSources(t *testing.T) (servedSources, string) {
	t.Helper()
	issued, err := exchangeByDelegation(presentedAs(t, identityOf(t, "ingestservice")), "ingest")
	require.NoError(t, err)
	return servedSources{
		t: t, token: issued.GetToken(), owner: delegationPerson,
		credential: identityOf(t, "reports"),
	}, business.SourceDelegationBindingID(delegationID, "ingestservice")
}

// The resolved binding carries every field a verifier compares, and each one is
// read from the issuer's records rather than echoed from the question: the
// binding is granted to the module that uses it, within that module's
// installation, at its revision and incarnation.
func TestAResolvedOperationBindingCarriesEveryFieldAVerifierCompares(t *testing.T) {
	installDelegationBindingService(t)
	sources, bindingID := delegationSources(t)

	binding, err := sources.OperationBinding(context.Background(), bindingID)

	require.NoError(t, err)
	require.Equal(t, bindingID, binding.ID)
	require.Equal(t, business.ModulePrincipalID("ingestservice"), binding.PrincipalID,
		"granted to the module that exercises it")
	require.Equal(t, "installation-current", binding.InstallationID)
	require.Equal(t, uint64(1), binding.Revision)
	require.Equal(t, uint64(1), binding.Incarnation)
	require.False(t, binding.Revoked)

	require.NoError(t, mustVerify(t, verifierOver(t, "reports", sources), sources.token))
}

// A WITHDRAWN BINDING is refused, which is the third revocation the issue names.
// The delegation is revoked after the capability was minted; the served binding
// says so, and the verifier refuses.
func TestAWithdrawnOperationBindingIsRefusedThroughTheServedSources(t *testing.T) {
	store := installDelegationBindingService(t)
	sources, bindingID := delegationSources(t)
	verifier := verifierOver(t, "reports", sources)
	require.NoError(t, mustVerify(t, verifier, sources.token))

	store.revoke(t)

	binding, err := sources.OperationBinding(context.Background(), bindingID)
	require.NoError(t, err)
	require.True(t, binding.Revoked, "the served binding says it was withdrawn")

	_, err = verifier.Verify(context.Background(), sources.token)
	require.ErrorIs(t, err, workcontext.ErrRevoked)
}

// mustVerify is the "it verified a moment ago" half of every refusal test above.
// Without it a test that refuses for the wrong reason — a misconfigured
// verifier, an unreachable key set — reads as the revocation working.
func mustVerify(t *testing.T, verifier *sdk.PinnedVerifier, token string) error {
	t.Helper()
	_, err := verifier.Verify(context.Background(), token)
	require.NoError(t, err, "the capability must verify before the issuer's state is moved under it")
	return nil
}
