package adapters

import (
	"context"
	"crypto/ed25519"
	"errors"
	"slices"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/wool"
	"github.com/codefly-dev/core/workcontext"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// Serving the live authority a Work Context is held against, to the composed
// module that is its CALLEE (issue #1107).
//
// A callee verifies at full strength. The checks it can make from a trust root
// alone — the encoding, the signature, the issuer, the audience, the window, the
// shape — are a forwarding hop's, and a hop holds nothing to continue with: no
// seal, no epoch, no revision, and nothing to consume a single-use nonce
// against. A module that stops there accepts a capability whose installation was
// revoked, whose build was replaced, whose binding was withdrawn or whose
// principal's epoch has moved, for as long as that capability lives.
//
// The missing half is the ISSUER'S live state, and this host is the issuer. So it
// serves it: one question per call, each the exact question core's Verifier asks
// its SealSource, RevisionSource or GrantSource, answered by handing that
// question to the very sources this host mints and verifies with
// (moduleAuthoritySources). One body, so a callee's verdict and an issuance
// cannot disagree about what "current" means — which is the property the whole
// seal rests on.
//
// EVERY QUESTION IS ABOUT ONE CAPABILITY, which the caller presents. That is
// what bounds this surface, and it is the correction an independent review of
// its first revision forced. That version took the tenant and the audience as
// request fields; no answer depended on either, so the audience "pin" guarded
// nothing and a module could read any principal's epoch, build or revision in
// its tenant. Now the host authenticates the presented capability, requires it
// to be addressed to one of the caller's OWN names and to the caller's own
// tenant, and answers only about the identifiers that capability carries.
//
// Nothing is minted, derived or CONSUMED here. A question is not a use — the
// same rule readBack keeps — so a single-use capability presented with a
// question is still its consumer's to spend.

// errUnansweredWorkContextAuthority is what a caller is told when the host could
// not answer: a lookup failed and nothing was decided. It carries no cause; the
// cause is logged.
const errUnansweredWorkContextAuthority = "the host could not answer this work context authority question"

// errRefusedWorkContextAuthority is what a caller is told when the host DID
// decide and the decision is no — the owner is no longer a member, the principal
// has no authorization record here, a module's installation is inactive, the
// delegation behind a binding is stale, the approved build cannot be named.
//
// It is FailedPrecondition, the code this host already answers a capability its
// live state has moved past with, and the one a consumer re-mints on. It is
// deliberately NOT the cause's own message: the question is about a THIRD party,
// while the authority layer's refusals are worded for a caller asking about
// itself — one of them names the subject's current tenant, which is another
// organization's id.
const errRefusedWorkContextAuthority = "the issuer refuses this work context authority question: the authority it asks about has changed"

// errUnaddressedWorkContextAuthority is a question that is not about the
// capability presented: an identifier the capability does not name, or a
// question about a grant or binding it does not carry. It is the caller's own
// bug — a client asking about one capability while presenting another — so it is
// named rather than silently answered about the capability's own values.
const errUnaddressedWorkContextAuthority = "this question is not about the capability presented with it"

// ResolveWorkContextAuthority is the module-facing half: it authenticates the
// calling module from the Work Context it forwards, resolves what that module
// may ask about, and only then hands the question to the issuer's own sources.
//
// The order is the authorization: the caller's identity comes from its signed
// credential and never from the request, and the live grant behind it is re-read
// on every call like every other path on this surface.
func (s *ModuleCapabilitiesServer) ResolveWorkContextAuthority(
	ctx context.Context, req *gen.ModuleResolveWorkContextAuthorityRequest,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
	ctx, releaseAuthority, fenceErr := service.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return nil, fenceErr
	}
	defer releaseAuthority()

	if err := Validate(req); err != nil {
		return nil, err
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	read, err := service.AuthorizeModuleWorkContextAuthorityRead(ctx, caller)
	if err != nil {
		return nil, err
	}
	return WorkContextSingleton().resolveWorkContextAuthority(ctx, read, req)
}

// resolveWorkContextAuthority answers one live-authority question about one
// presented capability.
//
// The three "absence is the answer" sentinels core defines — a principal that
// holds no such installation, a principal that bears no execution, a binding id
// that names nothing — are carried as STATES on the answer rather than as
// errors, because a verifier turns each into a REFUSAL of the capability and not
// into an outage. Of the rest, a DECISION is FailedPrecondition and a failed
// lookup is Unavailable; see the two message constants above for why that split
// is load-bearing rather than cosmetic.
func (s *WorkContextAuthorityServer) resolveWorkContextAuthority(
	ctx context.Context, read business.ModuleWorkContextAuthorityRead,
	req *gen.ModuleResolveWorkContextAuthorityRequest,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
	if !s.configured() {
		return nil, status.Error(codes.FailedPrecondition, "Work Context authority is not configured")
	}
	claims, err := s.addressedToCaller(ctx, req.GetWorkContext(), read.Audiences)
	if err != nil {
		return nil, err
	}
	// The tenant is the CALLER'S, and the capability's must be it. A module
	// installed in several organizations asks with the credential it minted for
	// the one in question, exactly as every other capability it exercises there
	// does.
	if claims.GetTenantId() != read.Tenant {
		return nil, status.Error(codes.PermissionDenied, "work context authority caller denied")
	}
	tenant := read.Tenant
	switch question := req.GetQuestion().(type) {
	case *gen.ModuleResolveWorkContextAuthorityRequest_Seal:
		return s.answerSeal(ctx, tenant, claims, question.Seal)
	case *gen.ModuleResolveWorkContextAuthorityRequest_PrincipalEpoch:
		return s.answerPrincipalEpoch(ctx, tenant, claims, question.PrincipalEpoch)
	case *gen.ModuleResolveWorkContextAuthorityRequest_OperationBinding:
		return s.answerOperationBinding(ctx, tenant, claims, question.OperationBinding)
	case *gen.ModuleResolveWorkContextAuthorityRequest_AuthorizationRevision:
		return s.answerAuthorizationRevision(ctx, tenant, claims, question.AuthorizationRevision)
	case *gen.ModuleResolveWorkContextAuthorityRequest_ApprovalGrant:
		return s.answerApprovalGrant(ctx, tenant, claims, question.ApprovalGrant)
	case *gen.ModuleResolveWorkContextAuthorityRequest_ApprovedBuild:
		return s.answerApprovedBuild(question.ApprovedBuild)
	default:
		// Unreachable while the schema requires the oneof, and answered rather
		// than panicked on: a question this host does not understand is a
		// question it must not guess at.
		return nil, status.Error(codes.InvalidArgument, "no work context authority question was asked")
	}
}

// addressedToCaller authenticates the presented capability and requires it to be
// addressed to one of the names the CALLER holds.
//
// The route target is the caller's own, never read off the token — which is the
// shape core's Inspector documents and requires, because comparing a token's
// audience to itself passes everything. A module holds at most two names, so the
// token is inspected once per name and refused when neither matches.
//
// Inspect is core's forwarding-hop entrypoint: the decode path, the issuer pin,
// the signature under this issuer's key, the window and the shape, with no live
// state read and nothing consumed. That is exactly the strength needed here —
// the live state is what this surface is about to answer, and consuming the
// capability would burn a nonce to answer a question.
//
// Decode beside it is core's own structural reader, over THE SAME BYTES Inspect
// authenticated, which is what makes the claims it returns the issuer's own
// statement rather than a caller's. It reimplements no check: Inspected
// deliberately carries only what a routing hop needs, and the owner, the actor
// chain and the binding are what a question has to be held against.
func (s *WorkContextAuthorityServer) addressedToCaller(
	ctx context.Context, encoded string, audiences []string,
) (*basev0.WorkContextV1, error) {
	inspector := &workcontext.Inspector{
		Issuer: s.issuer,
		Keys:   map[string]ed25519.PublicKey{s.keyID: s.publicKey},
		Now:    s.now,
	}
	addressed := false
	var refusal error
	for _, audience := range audiences {
		if _, err := inspector.Inspect(encoded, audience); err != nil {
			refusal = err
			continue
		}
		addressed = true
		break
	}
	if !addressed {
		// One refusal for every reason the capability was not admitted — another
		// audience, a bad signature, an expired window, a foreign encoding — with
		// the cause logged. A caller holding the token can read its own fields;
		// what it must not learn here is which of this host's checks refused it.
		wool.Get(ctx).In("ResolveWorkContextAuthority").Warn(
			"a presented capability was not addressed to the calling module",
			wool.ErrField(refusal))
		return nil, status.Error(codes.PermissionDenied, "work context authority caller denied")
	}
	decoded, err := workcontext.Decode(encoded)
	if err != nil {
		// Unreachable: Inspect runs the same decode path first. Answered rather
		// than ignored, because a disagreement between the two would mean one of
		// them is not reading the bytes the other authenticated.
		return nil, status.Error(codes.Internal, "the presented capability could not be read")
	}
	return decoded.Context(), nil
}

// chainPrincipals is every principal a verifier holds an epoch and an execution
// against: the task owner, and each actor hop. Core asks about exactly these and
// no others (checkActorEpochsAgainst, checkExecutionsAgainst).
func chainPrincipals(claims *basev0.WorkContextV1) []string {
	principals := []string{claims.GetOwnerPrincipalId()}
	for _, hop := range claims.GetActorChain() {
		principals = append(principals, hop.GetPrincipalId())
	}
	return principals
}

func (s *WorkContextAuthorityServer) answerSeal(
	ctx context.Context, tenant string, claims *basev0.WorkContextV1, question *gen.WorkContextSealQuestion,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
	// The seal is held for the task's OWNER, through the installation the
	// capability names: core asks about that pair and no other.
	if question.GetPrincipalId() != claims.GetOwnerPrincipalId() ||
		question.GetInstallationId() != claims.GetSeal().GetInstallationId() {
		return nil, status.Error(codes.InvalidArgument, errUnaddressedWorkContextAuthority)
	}
	seals, _, _ := s.moduleAuthoritySources(tenant, question.GetPrincipalId())
	live, err := seals.Seal(ctx, question.GetPrincipalId(), question.GetInstallationId())
	switch {
	case errors.Is(err, workcontext.ErrNoSeal):
		return sealAnswer(&gen.WorkContextSealAnswer{
			State:          gen.WorkContextSealState_WORK_CONTEXT_SEAL_STATE_UNHELD,
			InstallationId: question.GetInstallationId(),
		}), nil
	case err != nil:
		return nil, unansweredWorkContextAuthority(ctx, "seal", err)
	}
	return sealAnswer(&gen.WorkContextSealAnswer{
		State:                gen.WorkContextSealState_WORK_CONTEXT_SEAL_STATE_HELD,
		InstallationId:       live.InstallationID,
		InstallationRevision: live.InstallationRevision,
	}), nil
}

func (s *WorkContextAuthorityServer) answerPrincipalEpoch(
	ctx context.Context, tenant string, claims *basev0.WorkContextV1, question *gen.WorkContextPrincipalEpochQuestion,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
	if !slices.Contains(chainPrincipals(claims), question.GetPrincipalId()) {
		return nil, status.Error(codes.InvalidArgument, errUnaddressedWorkContextAuthority)
	}
	seals, _, _ := s.moduleAuthoritySources(tenant, question.GetPrincipalId())
	epoch, err := seals.PrincipalEpoch(ctx, question.GetPrincipalId())
	switch {
	// The same sentinel core's seal source returns for an installation nobody
	// holds answers a principal the organization has no record of, and the two
	// are separate states on the wire: a verifier compares an epoch for every
	// actor hop, and "this principal is not one the issuer holds" is the answer
	// that revokes a delegated actor.
	case errors.Is(err, workcontext.ErrNoSeal):
		return principalEpochAnswer(&gen.WorkContextPrincipalEpochAnswer{
			State:       gen.WorkContextPrincipalState_WORK_CONTEXT_PRINCIPAL_STATE_UNKNOWN,
			PrincipalId: question.GetPrincipalId(),
		}), nil
	case err != nil:
		return nil, unansweredWorkContextAuthority(ctx, "principal epoch", err)
	}
	return principalEpochAnswer(&gen.WorkContextPrincipalEpochAnswer{
		State:       gen.WorkContextPrincipalState_WORK_CONTEXT_PRINCIPAL_STATE_KNOWN,
		PrincipalId: question.GetPrincipalId(),
		Epoch:       epoch,
	}), nil
}

func (s *WorkContextAuthorityServer) answerOperationBinding(
	ctx context.Context, tenant string, claims *basev0.WorkContextV1, question *gen.WorkContextOperationBindingQuestion,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
	// The binding the capability is SEALED to, exactly. A capability carrying
	// none has no binding to ask about, and a binding some other capability
	// names is not this question's — resolving one would make this an oracle
	// over other modules' installations.
	sealed := claims.GetOperationBinding().GetBindingId()
	if sealed == "" || question.GetBindingId() != sealed {
		return nil, status.Error(codes.InvalidArgument, errUnaddressedWorkContextAuthority)
	}
	seals, _, _ := s.moduleAuthoritySources(tenant, claims.GetOwnerPrincipalId())
	live, err := seals.OperationBinding(ctx, question.GetBindingId())
	switch {
	case errors.Is(err, workcontext.ErrNoBinding):
		return operationBindingAnswer(&gen.WorkContextOperationBindingAnswer{
			State:     gen.WorkContextOperationBindingState_WORK_CONTEXT_OPERATION_BINDING_STATE_ABSENT,
			BindingId: question.GetBindingId(),
		}), nil
	case err != nil:
		return nil, unansweredWorkContextAuthority(ctx, "operation binding", err)
	}
	return operationBindingAnswer(&gen.WorkContextOperationBindingAnswer{
		State:          gen.WorkContextOperationBindingState_WORK_CONTEXT_OPERATION_BINDING_STATE_RESOLVED,
		BindingId:      live.ID,
		PrincipalId:    live.PrincipalID,
		InstallationId: live.InstallationID,
		Revision:       live.Revision,
		Incarnation:    live.Incarnation,
		Revoked:        live.Revoked,
	}), nil
}

func (s *WorkContextAuthorityServer) answerAuthorizationRevision(
	ctx context.Context, tenant string, claims *basev0.WorkContextV1,
	question *gen.WorkContextAuthorizationRevisionQuestion,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
	if question.GetOwnerPrincipalId() != claims.GetOwnerPrincipalId() {
		return nil, status.Error(codes.InvalidArgument, errUnaddressedWorkContextAuthority)
	}
	// The owner is bound into the source exactly as it is at a mint: this host's
	// revision is the greater of the organization's and the owner's own, so an
	// answer that ignored the owner would be a different number from the one the
	// capability carries.
	_, revisions, _ := s.moduleAuthoritySources(tenant, question.GetOwnerPrincipalId())
	revision, err := revisions.AuthorizationRevision(ctx, tenant)
	if err != nil {
		return nil, unansweredWorkContextAuthority(ctx, "authorization revision", err)
	}
	return &gen.ModuleResolveWorkContextAuthorityResponse{
		Answer: &gen.ModuleResolveWorkContextAuthorityResponse_AuthorizationRevision{
			AuthorizationRevision: &gen.WorkContextAuthorizationRevisionAnswer{
				OwnerPrincipalId: question.GetOwnerPrincipalId(),
				Revision:         revision,
			},
		},
	}, nil
}

func (s *WorkContextAuthorityServer) answerApprovalGrant(
	ctx context.Context, tenant string, claims *basev0.WorkContextV1,
	question *gen.WorkContextApprovalGrantQuestion,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
	claimed := claims.GetGrantHop().GetGrantId()
	if claimed == "" || question.GetGrantId() != claimed {
		return nil, status.Error(codes.InvalidArgument, errUnaddressedWorkContextAuthority)
	}
	_, _, grants := s.moduleAuthoritySources(tenant, claims.GetOwnerPrincipalId())
	grant, err := grants.Grant(ctx, question.GetGrantId())
	switch {
	// This host records no approval grants, which is a property of the host and
	// not of the id. It is served rather than left for a client to assume, so
	// the policy has one home: a capability carrying a grant hop is refused
	// because the issuer says it never made that decision.
	case errors.Is(err, ErrNoApprovalGrants):
		return &gen.ModuleResolveWorkContextAuthorityResponse{
			Answer: &gen.ModuleResolveWorkContextAuthorityResponse_ApprovalGrant{
				ApprovalGrant: &gen.WorkContextApprovalGrantAnswer{
					State:   gen.WorkContextApprovalGrantState_WORK_CONTEXT_APPROVAL_GRANT_STATE_NOT_RECORDED,
					GrantId: question.GetGrantId(),
				},
			},
		}, nil
	case err != nil:
		return nil, unansweredWorkContextAuthority(ctx, "approval grant", err)
	}
	// Unreachable while this host records none. It is an error rather than a
	// silently empty answer so that the day it does record them, the serving
	// path fails loudly here instead of telling every callee NOT_RECORDED about
	// a grant that exists.
	wool.Get(ctx).In("ResolveWorkContextAuthority").Warn(
		"this host answered an approval grant, which its grant source is not expected to record; " +
			"the served answer has no shape for one, so it is withheld")
	_ = grant
	return nil, status.Error(codes.Unimplemented,
		"this host resolved an approval grant and the module authority answer cannot carry one")
}

func sealAnswer(answer *gen.WorkContextSealAnswer) *gen.ModuleResolveWorkContextAuthorityResponse {
	return &gen.ModuleResolveWorkContextAuthorityResponse{
		Answer: &gen.ModuleResolveWorkContextAuthorityResponse_Seal{Seal: answer},
	}
}

func principalEpochAnswer(answer *gen.WorkContextPrincipalEpochAnswer) *gen.ModuleResolveWorkContextAuthorityResponse {
	return &gen.ModuleResolveWorkContextAuthorityResponse{
		Answer: &gen.ModuleResolveWorkContextAuthorityResponse_PrincipalEpoch{PrincipalEpoch: answer},
	}
}

func operationBindingAnswer(answer *gen.WorkContextOperationBindingAnswer) *gen.ModuleResolveWorkContextAuthorityResponse {
	return &gen.ModuleResolveWorkContextAuthorityResponse{
		Answer: &gen.ModuleResolveWorkContextAuthorityResponse_OperationBinding{OperationBinding: answer},
	}
}

// decidedWorkContextAuthority reports whether a source's failure is a DECISION
// about the authority the question asks about, rather than a lookup that did not
// run.
//
// The list is the host's own sentinels, each of which means the issuer looked and
// the answer is no: the principal has no authorization record in this
// organization (a removed member, which is the offboarding case this surface
// exists for), a module's installation is not active, the delegation behind a
// binding is stale, the execution principal is unknown or its approved build
// cannot be named, the credential's tenant is not current.
//
// A gRPC status the authority layer already decided counts too: PermissionDenied,
// FailedPrecondition and Unauthenticated out of a source are statements about the
// SUBJECT of the question. Unavailable keeps its meaning — the live read failed.
func decidedWorkContextAuthority(err error) bool {
	for _, decided := range []error{
		business.ErrWorkContextPrincipalUnknown,
		business.ErrWorkContextSealUnheld,
		business.ErrWorkContextNoAudienceInstallation,
		business.ErrModuleInstallationInactive,
		business.ErrModuleTenantNotCurrent,
		business.ErrSourceDelegationContextStale,
		business.ErrNoOperationBinding,
		business.ErrModuleHopStale,
		business.ErrUnknownExecutionPrincipal,
	} {
		if errors.Is(err, decided) {
			return true
		}
	}
	switch status.Code(err) {
	case codes.PermissionDenied, codes.FailedPrecondition, codes.Unauthenticated:
		return true
	default:
		return false
	}
}

// unansweredWorkContextAuthority maps a source failure that is NOT one of core's
// absence sentinels.
//
// Two answers and never a third. A DECISION is FailedPrecondition: the authority
// the question asks about has changed, so the capability is revoked and its
// holder mints again. Anything else is Unavailable: the lookup failed, nothing
// was decided, and a retry may succeed. Collapsing the first into the second —
// which the first revision of this file did — makes a permanent revocation read
// as a transient failure, which a retry policy retries forever.
//
// Neither carries the cause. These questions are about a third party, and the
// authority layer's messages are written for a caller asking about itself; one
// of them names the subject's current tenant. The cause is logged here.
func unansweredWorkContextAuthority(ctx context.Context, question string, err error) error {
	decided := decidedWorkContextAuthority(err)
	wool.Get(ctx).In("ResolveWorkContextAuthority").Warn(
		"a work context authority question was refused or could not be answered",
		wool.Field("question", question), wool.Field("decided", decided), wool.ErrField(err))
	if decided {
		return status.Error(codes.FailedPrecondition, errRefusedWorkContextAuthority)
	}
	return status.Error(codes.Unavailable, errUnansweredWorkContextAuthority)
}

// answerApprovedBuild answers the one question this host no longer has a record
// behind: which execution it APPROVES for a principal.
//
// core carried an approved-build seal until v0.19.6 removed the concept — a
// capability is no longer verified against the image its holder runs — so this
// host approves no build for anybody, and `bears no execution` is the true
// answer for every principal rather than a stand-in for one.
//
// It is answered rather than left to the switch's default because a consumer
// still built against a core that asks is not wrong to ask. Falling through
// gave it "no work context authority question was asked", which core reports as
// the PARENT failing to verify — so a module one core version behind refused
// every call with an error naming the caller's authority and never the skew.
//
// Measured on a validation cell: every chat route refused, for every person,
// for hours, with "caller does not hold the required authority". Nothing was
// wrong with anyone's authority; two repositories disagreed about whether a
// question exists.
//
// `bears no execution` is also the SAFE answer, which is why it can be given
// without a record: core's check reads it as "this principal bears no
// execution the issuer approves", and a capability that nonetheless carries one
// is REVOKED rather than admitted (checkExecutionAgainst). So a caller that
// seals an execution still fails closed; only a caller that seals none — which
// is every capability this host now mints — proceeds.
func (s *WorkContextAuthorityServer) answerApprovedBuild(
	question *gen.WorkContextApprovedBuildQuestion,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
	if question.GetPrincipalId() == "" {
		return nil, status.Error(codes.InvalidArgument, "approved build question names no principal")
	}
	return &gen.ModuleResolveWorkContextAuthorityResponse{
		Answer: &gen.ModuleResolveWorkContextAuthorityResponse_ApprovedBuild{
			ApprovedBuild: &gen.WorkContextApprovedBuildAnswer{
				State:       gen.WorkContextApprovedBuildState_WORK_CONTEXT_APPROVED_BUILD_STATE_BEARS_NO_EXECUTION,
				PrincipalId: question.GetPrincipalId(),
			},
		},
	}, nil
}
