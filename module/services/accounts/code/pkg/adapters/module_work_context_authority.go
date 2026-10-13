package adapters

import (
	"context"
	"errors"

	"github.com/codefly-dev/core/wool"
	"github.com/codefly-dev/core/workcontext"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
// WHAT TRAVELS AND WHAT DOES NOT. Each answer is one scalar fact about one named
// principal, installation, binding or grant. No capability is presented here and
// none is minted, nothing is consumed, and nothing in a request names a scope: a
// module asks about state it already holds a capability for, and the question is
// which of the issuer's counters that capability must equal.
//
// WHO MAY ASK is business.AuthorizeModuleWorkContextAuthorityRead's, beside the
// grant it reads — the caller's own tenant, the caller's own audience, nothing
// else.

// ResolveWorkContextAuthority is the module-facing half: it authenticates the
// calling module from the Work Context it forwards, decides whether that module
// may ask about this (tenant, audience) at all, and only then hands the question
// to the issuer's own sources.
//
// The order is the authorization: the caller's identity comes from the signed
// capability and never from the request, and the live grant behind it is re-read
// on every call like every other path on this surface — so a module whose
// installation was narrowed or withdrawn stops being able to resolve authority
// at its next question rather than at its credential's expiry.
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
	if err := service.AuthorizeModuleWorkContextAuthorityRead(
		ctx, caller, req.GetOrganizationId(), req.GetAudience()); err != nil {
		return nil, err
	}
	return WorkContextSingleton().ResolveWorkContextAuthority(ctx, req)
}

// errUnansweredWorkContextAuthority is what a caller is told when the host could
// not answer. It is deliberately one sentence with no cause in it: the cause is
// logged here, and a composed module's correct response to any of them is the
// same (retry, then refuse).
const errUnansweredWorkContextAuthority = "the host could not answer this work context authority question"

// ResolveWorkContextAuthority answers one live-authority question for a composed
// module's verifier.
//
// The three "absence is the answer" sentinels core defines — a principal that
// holds no such installation, a principal that bears no execution, a binding id
// that names nothing — are carried as STATES on the answer rather than as
// errors, because a verifier turns each into a REFUSAL of the capability and not
// into an outage. Everything else is an error: a status the authority layer
// already decided travels as it is, and anything else becomes Unavailable,
// because nothing was decided and a retry may succeed. Reading a failed lookup
// as "no such installation" would turn a database blip into mass revocation;
// reading it as "held" would be the check not running.
func (s *WorkContextAuthorityServer) ResolveWorkContextAuthority(
	ctx context.Context, req *gen.ModuleResolveWorkContextAuthorityRequest,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
	if !s.configured() {
		return nil, status.Error(codes.FailedPrecondition, "Work Context authority is not configured")
	}
	tenant := req.GetOrganizationId()
	switch question := req.GetQuestion().(type) {
	case *gen.ModuleResolveWorkContextAuthorityRequest_Seal:
		return s.answerSeal(ctx, tenant, question.Seal)
	case *gen.ModuleResolveWorkContextAuthorityRequest_PrincipalEpoch:
		return s.answerPrincipalEpoch(ctx, tenant, question.PrincipalEpoch)
	case *gen.ModuleResolveWorkContextAuthorityRequest_ApprovedBuild:
		return s.answerApprovedBuild(ctx, tenant, question.ApprovedBuild)
	case *gen.ModuleResolveWorkContextAuthorityRequest_OperationBinding:
		return s.answerOperationBinding(ctx, tenant, question.OperationBinding)
	case *gen.ModuleResolveWorkContextAuthorityRequest_AuthorizationRevision:
		return s.answerAuthorizationRevision(ctx, tenant, question.AuthorizationRevision)
	case *gen.ModuleResolveWorkContextAuthorityRequest_ApprovalGrant:
		return s.answerApprovalGrant(ctx, question.ApprovalGrant)
	default:
		// Unreachable while the schema requires the oneof, and answered rather
		// than panicked on: a question this host does not understand is a
		// question it must not guess at.
		return nil, status.Error(codes.InvalidArgument, "no work context authority question was asked")
	}
}

func (s *WorkContextAuthorityServer) answerSeal(
	ctx context.Context, tenant string, question *gen.WorkContextSealQuestion,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
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
	ctx context.Context, tenant string, question *gen.WorkContextPrincipalEpochQuestion,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
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

func (s *WorkContextAuthorityServer) answerApprovedBuild(
	ctx context.Context, tenant string, question *gen.WorkContextApprovedBuildQuestion,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
	seals, _, _ := s.moduleAuthoritySources(tenant, question.GetPrincipalId())
	digest, incarnation, err := seals.ApprovedBuild(ctx, question.GetPrincipalId())
	switch {
	// BEARS NO EXECUTION is the answer for a person's session, and it is not a
	// failure. It stays distinct from an unknown principal, which the source
	// refuses: answering "bears none" for an identity the host never heard of
	// would hand out a credential nothing can revoke by replacing a build.
	case errors.Is(err, workcontext.ErrNoApprovedBuild):
		return approvedBuildAnswer(&gen.WorkContextApprovedBuildAnswer{
			State:       gen.WorkContextApprovedBuildState_WORK_CONTEXT_APPROVED_BUILD_STATE_BEARS_NO_EXECUTION,
			PrincipalId: question.GetPrincipalId(),
		}), nil
	case err != nil:
		return nil, unansweredWorkContextAuthority(ctx, "approved build", err)
	}
	return approvedBuildAnswer(&gen.WorkContextApprovedBuildAnswer{
		State:            gen.WorkContextApprovedBuildState_WORK_CONTEXT_APPROVED_BUILD_STATE_APPROVED,
		PrincipalId:      question.GetPrincipalId(),
		ImageDigest:      digest,
		BuildIncarnation: incarnation,
	}), nil
}

func (s *WorkContextAuthorityServer) answerOperationBinding(
	ctx context.Context, tenant string, question *gen.WorkContextOperationBindingQuestion,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
	seals, _, _ := s.moduleAuthoritySources(tenant, "")
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
	ctx context.Context, tenant string, question *gen.WorkContextAuthorizationRevisionQuestion,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
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
	ctx context.Context, question *gen.WorkContextApprovalGrantQuestion,
) (*gen.ModuleResolveWorkContextAuthorityResponse, error) {
	_, _, grants := s.moduleAuthoritySources("", "")
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

func approvedBuildAnswer(answer *gen.WorkContextApprovedBuildAnswer) *gen.ModuleResolveWorkContextAuthorityResponse {
	return &gen.ModuleResolveWorkContextAuthorityResponse{
		Answer: &gen.ModuleResolveWorkContextAuthorityResponse_ApprovedBuild{ApprovedBuild: answer},
	}
}

func operationBindingAnswer(answer *gen.WorkContextOperationBindingAnswer) *gen.ModuleResolveWorkContextAuthorityResponse {
	return &gen.ModuleResolveWorkContextAuthorityResponse{
		Answer: &gen.ModuleResolveWorkContextAuthorityResponse_OperationBinding{OperationBinding: answer},
	}
}

// unansweredWorkContextAuthority maps a source failure that is NOT one of core's
// absence sentinels.
//
// A refusal the authority layer already decided keeps its own code and message —
// those are a wire contract with consuming modules, and re-wrapping one would
// turn a verdict about the caller into an outage. Anything else is Unavailable
// with its cause logged and not returned: a lookup that failed decided nothing,
// and the caller is told only that.
func unansweredWorkContextAuthority(ctx context.Context, question string, err error) error {
	if code := status.Code(err); code != codes.Unknown {
		return err
	}
	wool.Get(ctx).In("ResolveWorkContextAuthority").Warn(
		"a work context authority question could not be answered",
		wool.Field("question", question), wool.ErrField(err))
	return status.Error(codes.Unavailable, errUnansweredWorkContextAuthority)
}
