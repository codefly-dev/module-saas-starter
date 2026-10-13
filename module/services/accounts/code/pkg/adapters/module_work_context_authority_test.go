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
// capability that is already signed, in each of the three ways a revocation
// reaches one:
//
//   - the installation it is sealed to stops being held;
//   - the owner's epoch advances;
//   - the tenant's authorization revision advances.
//
// Each must be REFUSED, because the alternative is what this host shipped
// before: a module running a forwarding hop's inspection, which accepts all
// three until the capability expires.
//
// Nothing here fakes the surface under test. The verifier is the published one,
// the answers come out of the real RPC handler — authentication, live grant
// re-read and all — and the sources are as narrow as core's interfaces, so a
// test that passes proves the wire carries what a verifier needs.

const (
	authorityOrg          = "66666666-6666-4666-8666-666666666666"
	authorityPerson       = "77777777-7777-4777-8777-777777777777"
	authorityInstallation = "88888888-8888-4888-8888-888888888888"
	authorityOtherOrg     = "99999999-9999-4999-8999-999999999999"

	// Two declared modules, each a possible callee. documents is the one every
	// capability below is addressed to; reports is the one that must not be able
	// to resolve authority for documents' capabilities.
	authorityPrincipals = `{"documents":{"workload":{"service_account":"documents","namespace":"acme-prod","container":"app"},"tenant":"` +
		authorityOrg + `"},` +
		`"reports":{"workload":{"service_account":"reports","namespace":"acme-prod","container":"app"},"tenant":"` +
		authorityOrg + `"}}`
)

// servedSources is the composed module's side: core's three sources, each one
// RPC call to the host's module authority endpoint.
//
// It is as narrow as the interfaces it implements, and that is the point of
// those interfaces being narrow. It makes no decision — every answer is
// translated, never interpreted — and it refuses anything it does not
// understand: an answer on another arm than the question, a state the host did
// not set, or an answer about a principal, installation or binding other than
// the one asked about. A client that read an unexpected answer as a permissive
// one would turn the strongest check in the model into the easiest to defeat.
//
// `owner` is bound at construction, exactly as this host binds it on its own
// revision source: core's RevisionSource is keyed by tenant alone, and this
// host's revision is the greater of the organization's and the owner's own. The
// owner comes off the capability under verification, which is safe because a
// verifier consults no source until the signature over those very bytes has
// checked out.
type servedSources struct {
	t          *testing.T
	tenant     string
	audience   string
	owner      string
	credential string
}

func (s servedSources) ask(
	ctx context.Context, question any,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
	s.t.Helper()
	request := &gen.ModuleResolveWorkContextAuthorityRequest{
		OrganizationId: s.tenant,
		Audience:       s.audience,
	}
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

func (s servedSources) ApprovedBuild(ctx context.Context, principalID string) (string, uint64, error) {
	response, err := s.ask(ctx, &gen.WorkContextApprovedBuildQuestion{PrincipalId: principalID})
	if err != nil {
		return "", 0, err
	}
	answer := response.GetApprovedBuild()
	if answer == nil || answer.GetPrincipalId() != principalID {
		return "", 0, fmt.Errorf("the host answered no approved build for principal %q", principalID)
	}
	switch answer.GetState() {
	case gen.WorkContextApprovedBuildState_WORK_CONTEXT_APPROVED_BUILD_STATE_APPROVED:
		return answer.GetImageDigest(), answer.GetBuildIncarnation(), nil
	case gen.WorkContextApprovedBuildState_WORK_CONTEXT_APPROVED_BUILD_STATE_BEARS_NO_EXECUTION:
		return "", 0, workcontext.ErrNoApprovedBuild
	default:
		return "", 0, fmt.Errorf("the host answered no approved-build state")
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

func (s servedSources) AuthorizationRevision(ctx context.Context, tenantID string) (uint64, error) {
	if tenantID != s.tenant {
		return 0, fmt.Errorf("this source answers tenant %q, the capability names %q", s.tenant, tenantID)
	}
	response, err := s.ask(ctx, &gen.WorkContextAuthorizationRevisionQuestion{OwnerPrincipalId: s.owner})
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

// localRun is the transport posture a module's key-set admission reads. The
// key set here is served over loopback, which sdk-go admits on a local run and
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

// authorityFixture is one host with two declared modules installed, a person's
// capability addressed to one of them, and that module's own credential.
type authorityFixture struct {
	answers    *sealAnswersFake
	token      string
	owner      string
	credential string
	other      string
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
		answers:    answers,
		token:      token,
		owner:      authorityPerson,
		credential: mintModuleIdentity(t, business.ModulePrincipalID("documents"), authorityOrg),
		other:      mintModuleIdentity(t, business.ModulePrincipalID("reports"), authorityOrg),
	}
}

// sources is the client half, for an assertion about one answer rather than
// about a whole verification.
func (f *authorityFixture) sources(t *testing.T, credential string) servedSources {
	t.Helper()
	return servedSources{
		t: t, tenant: authorityOrg, audience: "documents", owner: f.owner, credential: credential,
	}
}

// verifier is the one a composed module builds: issuer and audience pinned, keys
// from an admitted endpoint, every source backed by the host, and a replay store
// of its own — single-use consumption is the consumer's, never the issuer's to
// do on its behalf.
func (f *authorityFixture) verifier(t *testing.T, credential string) *sdk.PinnedVerifier {
	t.Helper()
	sources := servedSources{
		t: t, tenant: authorityOrg, audience: "documents", owner: f.owner, credential: credential,
	}
	verifier, err := sdk.NewVerifier(sdk.VerifierSettings{
		Issuer:    "accounts.test",
		Audience:  "documents",
		Revisions: sources,
		Replay:    &workcontext.MemoryReplayStore{},
		Grants:    sources,
		Seals:     sources,
	}, serveKeySet(t))
	require.NoError(t, err)
	return verifier
}

// The headline: a module verifies a host-minted capability at full strength
// against the served sources. Every check core makes beyond a hop's inspection —
// the authorization revision, the sealed installation and its revision, the
// owner's epoch, the execution — is answered over the wire here.
func TestAComposedModuleVerifiesAHostCapabilityAgainstTheServedSources(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)

	verified, err := fixture.verifier(t, fixture.credential).Verify(context.Background(), fixture.token)

	require.NoError(t, err)
	require.Equal(t, authorityOrg, verified.Context().GetTenantId())
	require.Equal(t, authorityPerson, verified.Context().GetOwnerPrincipalId())
	require.Equal(t, authorityInstallation, verified.Context().GetSeal().GetInstallationId())
}

// A revoked installation: the capability is signed, unexpired and addressed
// here, and the organization no longer holds the installation it is sealed to.
// Before the sources were served this verified anywhere for its whole lifetime.
func TestAnUnheldInstallationIsRefusedThroughTheServedSources(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	verifier := fixture.verifier(t, fixture.credential)
	require.NoError(t, mustVerify(t, verifier, fixture.token))
	sources := fixture.sources(t, fixture.credential)

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
	verifier := fixture.verifier(t, fixture.credential)
	require.NoError(t, mustVerify(t, verifier, fixture.token))

	sources := fixture.sources(t, fixture.credential)

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
	verifier := fixture.verifier(t, fixture.credential)
	require.NoError(t, mustVerify(t, verifier, fixture.token))

	sources := fixture.sources(t, fixture.credential)

	fixture.answers.setRevision(13)

	revision, err := sources.AuthorizationRevision(context.Background(), authorityOrg)
	require.NoError(t, err)
	require.Equal(t, uint64(13), revision, "the served revision is the one that moved")

	_, err = verifier.Verify(context.Background(), fixture.token)
	require.ErrorIs(t, err, workcontext.ErrRevoked)
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
	verifier := fixture.verifier(t, fixture.credential)
	require.NoError(t, mustVerify(t, verifier, fixture.token))
	sources := fixture.sources(t, fixture.credential)

	fixture.answers.unknown[authorityPerson] = true

	_, err := sources.PrincipalEpoch(context.Background(), authorityPerson)
	require.ErrorIs(t, err, workcontext.ErrNoSeal)

	_, err = verifier.Verify(context.Background(), fixture.token)
	require.Error(t, err)
	require.NotErrorIs(t, err, workcontext.ErrNoApprovedBuild,
		"an unknown principal must not read as one that bears no execution")
}

// The one authorization this surface adds: a module resolves live authority for
// capabilities addressed to IT, and for nobody else's.
//
// Both halves are asserted, because each stands alone. Another module's audience
// is refused however legitimate the caller is; and the caller's own audience is
// refused in a tenant its credential is not bound to.
func TestAModuleCannotResolveAnotherModulesAuthority(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)

	// reports is a declared, installed, running module with a valid credential
	// of its own. It may not resolve the authority behind documents' audience.
	foreign := servedSources{
		t: t, tenant: authorityOrg, audience: "documents", owner: fixture.owner, credential: fixture.other,
	}
	_, err := foreign.Seal(context.Background(), authorityPerson, authorityInstallation)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// And the whole verification fails rather than degrading: a module that
	// cannot resolve the live state refuses the capability.
	verifier := fixture.verifier(t, fixture.other)
	_, err = verifier.Verify(context.Background(), fixture.token)
	require.Error(t, err)

	// Its own audience, another organization: refused on the tenant.
	elsewhere := servedSources{
		t: t, tenant: authorityOtherOrg, audience: "reports", owner: fixture.owner, credential: fixture.other,
	}
	_, err = elsewhere.PrincipalEpoch(context.Background(), authorityPerson)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// The identity comes from the signed capability, never from the request, so a
// caller that reaches this listener with no credential resolves nothing.
func TestResolvingAuthorityWithoutAModuleCredentialIsUnauthenticated(t *testing.T) {
	installWorkContextAuthorityFixture(t)

	_, err := ModuleCapabilitiesSingleton().ResolveWorkContextAuthority(context.Background(),
		&gen.ModuleResolveWorkContextAuthorityRequest{
			OrganizationId: authorityOrg,
			Audience:       "documents",
			Question: &gen.ModuleResolveWorkContextAuthorityRequest_PrincipalEpoch{
				PrincipalEpoch: &gen.WorkContextPrincipalEpochQuestion{PrincipalId: authorityPerson},
			},
		})

	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

// A request that asks nothing is refused by the schema. The oneof is required
// because an empty question has no safe default: answering the first arm would
// resolve something the caller never asked about.
func TestResolvingAuthorityWithNoQuestionIsInvalid(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)

	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(workContextHeader, fixture.credential))
	_, err := ModuleCapabilitiesSingleton().ResolveWorkContextAuthority(ctx,
		&gen.ModuleResolveWorkContextAuthorityRequest{
			OrganizationId: authorityOrg,
			Audience:       "documents",
		})

	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// This host records no approval grants, and it SAYS so rather than leaving a
// client to assume it: a capability carrying a grant hop names a decision the
// issuer never made, and its verifier refuses it.
func TestTheServedGrantSourceRecordsNoApprovals(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	sources := fixture.sources(t, fixture.credential)

	response, err := sources.ask(context.Background(),
		&gen.WorkContextApprovalGrantQuestion{GrantId: "an-approval-nobody-made"})
	require.NoError(t, err)
	require.Equal(t,
		gen.WorkContextApprovalGrantState_WORK_CONTEXT_APPROVAL_GRANT_STATE_NOT_RECORDED,
		response.GetApprovalGrant().GetState())

	grant, err := sources.Grant(context.Background(), "an-approval-nobody-made")
	require.Error(t, err, "a grant this issuer never recorded is a refusal, not an empty grant")
	require.Nil(t, grant)
}

// An operation binding id that names nothing is ABSENT, which a verifier turns
// into a refusal rather than an outage. An id is a pointer and never authority,
// so guessing one lands here.
func TestAnUnknownOperationBindingIsAbsentThroughTheServedSources(t *testing.T) {
	fixture := installWorkContextAuthorityFixture(t)
	sources := fixture.sources(t, fixture.credential)

	_, err := sources.OperationBinding(context.Background(), "source-delegation/guessed/documents")

	require.ErrorIs(t, err, workcontext.ErrNoBinding)
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
	sources := fixture.sources(t, fixture.credential)
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

	servedRevision, err := sources.AuthorizationRevision(context.Background(), authorityOrg)
	require.NoError(t, err)
	directRevision, err := issuerRevisions.AuthorizationRevision(context.Background(), authorityOrg)
	require.NoError(t, err)
	require.Equal(t, directRevision, servedRevision)

	// And the move is visible through both at once, so the equality above is not
	// two constants agreeing.
	fixture.answers.setRevision(21)
	servedRevision, err = sources.AuthorizationRevision(context.Background(), authorityOrg)
	require.NoError(t, err)
	directRevision, err = issuerRevisions.AuthorizationRevision(context.Background(), authorityOrg)
	require.NoError(t, err)
	require.Equal(t, directRevision, servedRevision)
	require.Equal(t, uint64(21), servedRevision)
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
