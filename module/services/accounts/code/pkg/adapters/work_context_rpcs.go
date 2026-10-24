package adapters

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// maxWorkContextReplayHorizon bounds how far into the future a consumer may
// declare a single-use capability's expiry. It sits well above the 900s issuance
// TTL cap (with ample room for clock skew) so it never rejects a real token,
// while still keeping a miswired caller from pinning a replay marker for long.
const maxWorkContextReplayHorizon = time.Hour

type WorkContextAuthorityConfiguration struct {
	Issuer     string
	KeyID      string
	PrivateKey []byte
	Authority  business.WorkContextAuthorityStore
	// Seals answers the live seal, epoch, execution and revision every
	// capability is minted against and verified against. Required: core has no
	// mode in which they are skipped, so a host without them issues nothing.
	Seals workContextSealAnswers
}

// WorkContextAuthorityServer is the permissions-plugin-owned issuer. It never
// owns consumer Task/Session rows: it binds current Accounts identity and RBAC
// facts into a short-lived Codefly capability, minted and verified by
// core/workcontext (work_context_core.go).
type WorkContextAuthorityServer struct {
	gen.UnimplementedWorkContextServiceServer

	issuer       string
	keyID        string
	key          ed25519.PrivateKey
	publicKey    ed25519.PublicKey
	seals        workContextSealAnswers
	authority    business.WorkContextAuthorityStore
	consumer     business.WorkContextConsumerAuthorityStore
	journal      business.ActorChainJournal
	replay       business.WorkContextReplayStore
	boundaries   business.SolutionRuntimeBoundarySeedStore
	configureErr error
	// now is the clock core mints and verifies on. Nil means the wall clock.
	now func() time.Time
}

func (s *WorkContextAuthorityServer) Configure(config WorkContextAuthorityConfiguration) {
	s.issuer = strings.TrimSpace(config.Issuer)
	s.keyID = ""
	s.key = nil
	s.publicKey = nil
	s.seals = nil
	s.authority = nil
	s.consumer = nil
	s.journal = nil
	s.replay = nil
	s.boundaries = nil
	s.configureErr = nil

	if s.issuer == "" || strings.TrimSpace(config.KeyID) == "" {
		s.configureErr = errors.New("work-context issuer and key ID are required")
		return
	}
	if len(config.PrivateKey) != ed25519.PrivateKeySize {
		s.configureErr = fmt.Errorf("work-context Ed25519 private key must be %d bytes", ed25519.PrivateKeySize)
		return
	}
	if config.Authority == nil {
		s.configureErr = errors.New("accounts store does not implement WorkContextAuthorityStore")
		return
	}
	if config.Seals == nil {
		s.configureErr = errors.New("no Work Context seal answers are wired, so no capability can be minted or verified")
		return
	}
	privateKey := append(ed25519.PrivateKey(nil), config.PrivateKey...)
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		s.configureErr = errors.New("work-context signing key has no Ed25519 public key")
		return
	}
	s.keyID = config.KeyID
	s.key = privateKey
	s.publicKey = publicKey
	s.seals = config.Seals
	s.authority = config.Authority
	s.consumer, _ = config.Authority.(business.WorkContextConsumerAuthorityStore)
	s.journal, _ = config.Authority.(business.ActorChainJournal)
	s.replay, _ = config.Authority.(business.WorkContextReplayStore)
	s.boundaries, _ = config.Authority.(business.SolutionRuntimeBoundarySeedStore)
}

// CheckAuthorizationRevision answers ONE question — is the authority this
// Work Context asserts still current — for every context, by resolving each
// SUBJECT with the resolver that knows it. Which resolver applies is decided
// by a fact the context carries, never by the shape of its subjects:
//
//	the context's own binding names a delegation  -> the delegation
//	the owner is itself a declared module         -> that module's installation
//	a subject is a declared module (a hop)        -> the binding that hop names
//	everyone else (people, agents)                -> the authority store
//
// Before this, a module hop that named no binding matched none of the three
// checks that existed, so it fell to the store — which does not hold module
// principals, answered "not found", and had the module calling report to its
// caller a revocation that had never happened. That denied every call an
// orchestrator makes to an owner it executes an operation for.
//
// Every refusal is PermissionDenied: a consumer must read a changed or forged
// context as denied, never as an authority outage it may retry.
func (s *WorkContextAuthorityServer) CheckAuthorizationRevision(
	ctx context.Context,
	req *gen.CheckAuthorizationRevisionRequest,
) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	if len(req.GetSubjects()) == 0 ||
		req.GetSubjects()[0].GetPrincipalId() != req.GetOwnerPrincipalId() {
		return nil, status.Error(codes.InvalidArgument, "owner must be the first revision subject")
	}
	// PHASE ONE of the cutover: a consumer that states NEITHER field is still
	// judged, not refused as malformed. Requiring the assertion is phase two,
	// after every consumer states it; refusing it now answers InvalidArgument
	// to every consumer generated before the fields existed, and a consumer
	// that maps only the denial codes reports that as an authority outage.
	// TestTheOldestSupportedClientIsStillAnswered holds this open.
	if req.GetOperationBindingId() != "" && req.GetSealsNoBinding() {
		return nil, status.Error(codes.InvalidArgument,
			"a context either names an operation binding or seals none; this request says both")
	}
	if err := s.confirmCurrentAuthority(ctx, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// confirmCurrentAuthority is the one dispatch. Each branch is a resolver that
// reads the authority it knows about, live; none of them infers a context's
// kind from what the others declined.
func (s *WorkContextAuthorityServer) confirmCurrentAuthority(
	ctx context.Context, req *gen.CheckAuthorizationRevisionRequest,
) error {
	// A context owned by a declared module has no row-backed authority at all:
	// it is its installation's, and confirmed against the module's current
	// declaration.
	if handled, err := checkModuleOperationContextRevision(ctx, req); handled {
		return err
	}
	// A context whose binding names a source delegation is authorized by that
	// delegation, whatever its subjects look like.
	if business.NamesASourceDelegation(req.GetOperationBindingId()) {
		if handled, err := checkSourceDelegationContextRevision(ctx, req); handled {
			return err
		}
	}
	seen := make(map[string]struct{}, len(req.GetSubjects()))
	subjects := make([]business.WorkContextRevisionSubject, 0, len(req.GetSubjects()))
	for _, subject := range req.GetSubjects() {
		if subject == nil {
			return status.Error(codes.InvalidArgument, "revision subject must not be null")
		}
		principalID := subject.GetPrincipalId()
		if _, duplicate := seen[principalID]; duplicate {
			return status.Error(codes.InvalidArgument, "revision subjects must be unique")
		}
		seen[principalID] = struct{}{}
		// A module hop, confirmed from the binding it names.
		if service != nil && principalID != req.GetOwnerPrincipalId() && service.IsDeclaredModulePrincipal(principalID) {
			if err := service.ConfirmModuleHopBinding(ctx, req.GetOrgId(), principalID,
				req.GetOperationBindingId(), moduleScopesOf(subject)); err != nil {
				wool.Get(ctx).In("CheckAuthorizationRevision").Warn("a module hop of this Work Context no longer stands",
					wool.Field("org", req.GetOrgId()), wool.Field("owner", req.GetOwnerPrincipalId()),
					wool.Field("module", principalID), wool.Field("binding", req.GetOperationBindingId()),
					wool.ErrField(err))
				return status.Error(codes.PermissionDenied, business.ErrModuleHopStale.Error())
			}
			continue
		}
		permissions, _, err := workContextScopes(subject.GetScopes(), recheckContentReads())
		if err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		subjects = append(subjects, business.WorkContextRevisionSubject{
			PrincipalID: principalID,
			Permissions: permissions,
		})
	}
	// Only a subject the STORE must answer for needs the store.
	if len(subjects) == 0 {
		return nil
	}
	if s == nil || s.consumer == nil {
		return status.Error(codes.Unavailable, "Work Context consumer authority is unavailable")
	}
	ctx = accountsauth.WithVerifiedDatabaseIdentity(ctx, req.GetOwnerPrincipalId(), req.GetOrgId())
	err := s.consumer.CheckWorkContextAuthorizationRevision(
		ctx, req.GetOrgId(), req.GetOwnerPrincipalId(), req.GetAuthorizationRevision(), subjects,
	)
	if err != nil {
		// Named where it is decided: a module reporting "the caller's
		// authorization was revoked" otherwise sends an operator looking for a
		// revocation that never happened.
		wool.Get(ctx).In("CheckAuthorizationRevision").Warn("the authority this Work Context asserts is no longer current",
			wool.Field("org", req.GetOrgId()), wool.Field("owner", req.GetOwnerPrincipalId()),
			wool.Field("sealed_revision", req.GetAuthorizationRevision()),
			wool.Field("subjects", len(req.GetSubjects())), wool.ErrField(err))
		return mapConsumerAuthorityError(err)
	}
	return nil
}

// moduleScopesOf is one subject's scopes in the module vocabulary.
func moduleScopesOf(subject *gen.WorkContextRevisionSubject) []business.ModuleOperationScope {
	scopes := make([]business.ModuleOperationScope, 0, len(subject.GetScopes()))
	for _, scope := range subject.GetScopes() {
		scopes = append(scopes, business.ModuleOperationScope{
			ResourceKind: scope.GetResourceKind(),
			Actions:      append([]string(nil), scope.GetActions()...),
			ResourceIDs:  append([]string(nil), scope.GetResourceIds()...),
		})
	}
	return scopes
}

// checkModuleOperationContextRevision confirms a context MintModuleOperationContext
// issued. Its owner and sole actor are the same module principal, so — unlike
// the store path — the owner legitimately appears twice among the subjects
// (once with the authority scopes, once as the actor hop with its granted
// scopes). Every refusal is PermissionDenied: a consumer must read a changed or
// forged module context as denied, never as an authority outage it may retry.
func checkModuleOperationContextRevision(ctx context.Context, req *gen.CheckAuthorizationRevisionRequest) (bool, error) {
	if service == nil {
		return false, nil
	}
	subjects := make([]business.ModuleOperationRevisionSubject, 0, len(req.GetSubjects()))
	for _, subject := range req.GetSubjects() {
		scopes := make([]business.ModuleOperationScope, 0, len(subject.GetScopes()))
		for _, scope := range subject.GetScopes() {
			scopes = append(scopes, business.ModuleOperationScope{
				ResourceKind: scope.GetResourceKind(),
				Actions:      append([]string(nil), scope.GetActions()...),
				ResourceIDs:  append([]string(nil), scope.GetResourceIds()...),
			})
		}
		subjects = append(subjects, business.ModuleOperationRevisionSubject{
			PrincipalID: subject.GetPrincipalId(),
			Scopes:      scopes,
		})
	}
	handled, err := service.CheckModuleOperationContextRevision(
		ctx, req.GetOrgId(), req.GetOwnerPrincipalId(), req.GetAuthorizationRevision(), subjects,
	)
	if !handled {
		return false, nil
	}
	if err != nil {
		// A registry this replica cannot vouch for is not a revocation: the
		// consumer is told to retry rather than to drop a context that may
		// still be current.
		if status.Code(err) == codes.Unavailable {
			return true, err
		}
		return true, status.Error(codes.PermissionDenied, business.ErrModuleOperationContextStale.Error())
	}
	return true, nil
}

func (s *WorkContextAuthorityServer) AuthorizeEvidenceRead(
	ctx context.Context,
	req *gen.AuthorizeEvidenceReadRequest,
) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.consumer == nil {
		return nil, status.Error(codes.Unavailable, "Evidence read authority is unavailable")
	}
	ctx = accountsauth.WithVerifiedDatabaseIdentity(
		ctx,
		req.GetCallerPrincipalId(),
		req.GetOrgId(),
	)
	err := s.consumer.AuthorizeEvidenceRead(
		ctx,
		req.GetOrgId(),
		req.GetCallerPrincipalId(),
		req.GetOwnerPrincipalId(),
		req.GetTaskId(),
		req.GetSessionId(),
	)
	if err != nil {
		return nil, mapConsumerAuthorityError(err)
	}
	return &emptypb.Empty{}, nil
}

// ConsumeSingleUse claims a SINGLE_USE Work Context exactly once. The consumer
// calls it after cryptographically verifying the capability, passing the token's
// own nonce as the context id; the first claim wins and every replay fails closed
// with AlreadyExists.
func (s *WorkContextAuthorityServer) ConsumeSingleUse(
	ctx context.Context,
	req *gen.ConsumeSingleUseWorkContextRequest,
) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.replay == nil {
		return nil, status.Error(codes.Unavailable, "Work Context replay store is unavailable")
	}
	expiresAt := req.GetExpiresAt().AsTime()
	// A Work Context TTL is capped at 900s at issuance, so a real capability's
	// expiry is always minutes out. Reject an expiry beyond a generous horizon so
	// a miswired consumer cannot pin a replay marker past its purge cadence.
	if expiresAt.After(time.Now().Add(maxWorkContextReplayHorizon)) {
		return nil, status.Error(codes.InvalidArgument, "expires_at is too far in the future for a Work Context")
	}
	err := s.replay.ConsumeSingleUseWorkContext(
		ctx,
		req.GetOrgId(),
		req.GetContextId(),
		expiresAt,
	)
	switch {
	case err == nil:
		return &emptypb.Empty{}, nil
	case errors.Is(err, business.ErrWorkContextAlreadyConsumed):
		return nil, status.Error(codes.AlreadyExists, business.ErrWorkContextAlreadyConsumed.Error())
	case errors.Is(err, context.Canceled):
		return nil, status.Error(codes.Canceled, "work context consume canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return nil, status.Error(codes.DeadlineExceeded, "work context consume timed out")
	default:
		return nil, status.Error(codes.Internal, "cannot consume single-use Work Context")
	}
}

func mapConsumerAuthorityError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "consumer authority check canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "consumer authority check timed out")
	case errors.Is(err, business.ErrWorkContextAuthorizationStale):
		return status.Error(codes.FailedPrecondition, business.ErrWorkContextAuthorizationStale.Error())
	case errors.Is(err, business.ErrEvidenceReadDenied):
		return status.Error(codes.PermissionDenied, business.ErrEvidenceReadDenied.Error())
	default:
		return status.Error(codes.Internal, "cannot resolve current consumer authority")
	}
}

// taskBoundary resolves the runtime boundary a capability is sealed under: the
// task_id a consumer reads to decide which runs it may see.
//
// There are two kinds of mint and the request cannot tell them apart; the
// credential can. An ORDINARY mint — every mint the host's own pages and every
// composed module make — names its own Task. A SOLUTION-SCOPED mint is one the
// gateway proved came from a registered solution, by verifying that solution's
// signed, solution-bound registration credential; such a mint gets the boundary
// the host derived for that solution and this organization, and naming a
// task_id is refused rather than ignored.
//
// Why the host has to assign it (issue #1015): a run is reachable only under
// its admitting boundary, so a solution needs a boundary that outlives one
// context. A boundary the solution could choose would let one solution mint for
// another's and read, answer and recover its runs.
//
// And why an ordinary mint may NOT name one either. A boundary is not a secret
// a consumer keeps: a consuming module that serves durable runs reports, on a
// run it lets a person read, the Work Context task that run was admitted under.
// So one read any caller is entitled to would otherwise hand them a stable
// value they could name on an ordinary mint and reach that solution's runs
// with, for as long as the registration lives — and a solution's passthrough
// forwards the viewer's bearer to its own backend, so that caller can mint.
// refuseRegisteredBoundary closes it, which leaves the credential the only way
// to obtain a solution's boundary. It is a refusal and not a silent override
// because a caller that named one either means to reach another solution's runs
// or is miswired into a boundary it does not own, and both deserve an error
// rather than a capability for something else.
//
// What this does NOT decide is which viewer may see which run inside one
// solution and organization: the owner is the person in the capability, and
// separating two people's runs under one boundary stays the consumer's own
// owner check. A sealed solution claim the consumer scoped by would remove that
// caveat; it is tracked on the consuming module's own tracker, which this
// repository may not name (root AGENTS.md, naming and confidentiality).
func (s *WorkContextAuthorityServer) taskBoundary(
	ctx context.Context, orgID, requested string,
) (string, error) {
	identity, scoped := accountsauth.VerifiedSolution(ctx)
	if !scoped {
		if requested == "" {
			return "", status.Error(codes.InvalidArgument, "task_id is required")
		}
		if err := s.refuseRegisteredBoundary(ctx, orgID, requested); err != nil {
			return "", err
		}
		return requested, nil
	}
	if requested != "" {
		return "", status.Error(
			codes.InvalidArgument,
			"a solution-scoped Work Context may not name a task_id: the host assigns the solution's runtime boundary",
		)
	}
	if s.boundaries == nil {
		return "", status.Error(codes.Unavailable, "solution runtime boundaries are unavailable")
	}
	seed, err := s.boundaries.SolutionRuntimeBoundarySeed(ctx, identity.SolutionID)
	if err != nil {
		return "", mapSolutionBoundaryError(err)
	}
	// The publisher is checked, not assumed. The credential binds a solution id
	// to the publisher that holds its secret; the registry binds the same id to
	// the publisher that claimed it. A secret re-provisioned to a different
	// publisher would otherwise mint the boundary of a registration it cannot
	// write — the registry refuses its writes, and this refuses its mints.
	if seed.Publisher != identity.Publisher {
		return "", status.Error(codes.PermissionDenied, "solution registration is owned by another publisher")
	}
	// "Serving" is DELIVERED presence and nothing else: the seed read above
	// refused a tombstone and a missing record by name, and the applied
	// generation is what the gateway resolves the solution's address from.
	// There is no lease to renew — the renewal path is deleted and a tombstone
	// is the only withdrawal.
	boundary, err := business.SolutionRuntimeBoundary(seed.BindingID, orgID)
	if err != nil {
		return "", status.Error(codes.Internal, "cannot derive the solution runtime boundary")
	}
	return boundary, nil
}

// refuseRegisteredBoundary refuses a caller-named task_id that is any
// registered solution's boundary. See taskBoundary for why an ordinary mint may
// not name one.
//
// It fails CLOSED: a store that cannot answer means the capability cannot be
// shown not to be a solution's, and signing it anyway is the whole hole.
func (s *WorkContextAuthorityServer) refuseRegisteredBoundary(
	ctx context.Context, orgID, requested string,
) error {
	if s.boundaries == nil {
		return status.Error(codes.Unavailable, "solution runtime boundaries are unavailable")
	}
	seeds, err := s.boundaries.SolutionRuntimeBoundarySeeds(ctx)
	if err != nil {
		return mapSolutionBoundaryError(err)
	}
	if business.IsSolutionRuntimeBoundary(seeds, requested, orgID) {
		// Deliberately does not say which solution: the caller learns only that
		// the value is not theirs to name.
		return status.Error(
			codes.InvalidArgument,
			"task_id is a registered solution's runtime boundary and may only be minted with that solution's credential",
		)
	}
	return nil
}

func mapSolutionBoundaryError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, business.ErrSolutionRegistrationNotFound):
		return status.Error(codes.FailedPrecondition, "solution is not registered")
	case errors.Is(err, business.ErrSolutionRegistrationTombstoned):
		return status.Error(codes.FailedPrecondition, business.ErrSolutionRegistrationTombstoned.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "solution runtime boundary lookup canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "solution runtime boundary lookup timed out")
	default:
		return status.Error(codes.Internal, "cannot resolve the solution runtime boundary")
	}
}

func (s *WorkContextAuthorityServer) StartTask(
	ctx context.Context,
	req *gen.StartTaskWorkContextRequest,
) (*gen.IssuedWorkContext, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	// A CONFINED MINT IS DECIDED ON ITS CONSUMER'S LIVE AUTHORITY, and that
	// decision has to hold until the capability is signed: an uninstall or a
	// narrowed installation committed in between would otherwise leave a
	// capability minted on consent that no longer exists. So the authority use
	// span opens here, before any other transaction this mint takes — the
	// order the fence connection is reserved in — and the consumer's ceiling
	// is read inside it (business.ConsumerInvokeCeiling) and released only
	// when this returns. The span holds the installation, its target and the
	// envelope revision, not the in-memory registry a redelivery replaces; that
	// half is re-checked after signing (business.ConsumerCeilingStillCurrent).
	if _, confined := accountsauth.VerifiedResourceConfinement(ctx); confined && service != nil {
		var releaseAuthority func()
		var fenceErr error
		ctx, releaseAuthority, fenceErr = service.BeginModuleAuthorityUse(ctx)
		if fenceErr != nil {
			return nil, fenceErr
		}
		defer releaseAuthority()
	}
	if err := requireVocabularyAudience(ctx, req.GetAudience()); err != nil {
		return nil, err
	}
	permissions, scopes, err := workContextScopes(req.GetAuthorityScopes(), mintContentReads(req.GetAudience()))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	// A CONFINED CREDENTIAL IS HELD TO ITS CONSUMER'S CEILING, before the
	// viewer's own authority is resolved at all.
	//
	// The order is the point, not a convenience. This is a ceiling, so a mint
	// outside it must be refused whatever the person could otherwise have
	// minted, and the refusal must say which rule turned it away. Running it
	// after the authority resolution would let an incidental refusal arrive
	// first and read as the reason — the organization binding in particular is
	// also enforced, for its own purposes, by the membership lookup's verified
	// database scope, and that refusal names a database scope rather than a
	// consumer. Runs for a resource-confined request only; every other mint
	// reaches the viewer's authority below exactly as before.
	consumerCeiling, err := s.holdConfinedMintToItsConsumer(ctx, req, permissions, scopes)
	if err != nil {
		return nil, err
	}
	ownerID, err := s.authorizeOwner(ctx, req.GetOrgId())
	if err != nil {
		return nil, err
	}
	taskID, err := s.taskBoundary(ctx, req.GetOrgId(), req.GetTaskId())
	if err != nil {
		return nil, err
	}
	facts, err := s.resolveAuthority(
		ctx, req.GetOrgId(), ownerID, req.GetActorPrincipalId(), permissions,
	)
	if err != nil {
		return nil, err
	}
	if err := enforceActorCeiling(ctx, facts.Actor, req.GetAudience(), scopes); err != nil {
		return nil, err
	}
	installation, err := s.audienceInstallation(ctx, req.GetOrgId(), req.GetAudience())
	if err != nil {
		return nil, err
	}

	request := mintRequest{
		Start: workcontext.StartInput{
			TenantID:           req.GetOrgId(),
			OwnerPrincipalID:   ownerID,
			OwnerPrincipalKind: business.PrincipalKindHuman,
			OrganizationID:     req.GetOrgId(),
			TaskID:             taskID,
			Audience:           req.GetAudience(),
			AuthorityScopes:    scopes,
			AttributionTeamIDs: facts.AttributionTeamIDs,
			WorkspaceID:        req.GetWorkspaceId(),
			ProjectID:          req.GetProjectId(),
			InstallationID:     installation,
		},
		// The capability opens the session the caller authenticated with, and
		// never one the request named: see callerSession.
		Session: callerSession(ctx),
		Replay:  workContextReplayPolicy(req.GetReplayPolicy()),
		TTL:     workContextTTL(req.GetTtlSeconds()),
	}
	if facts.Actor != nil {
		request.Hops = []delegationHop{{
			PrincipalID:   facts.Actor.ID,
			PrincipalKind: facts.Actor.Kind,
			AgentID:       facts.Actor.AgentIdentifier,
			// The per-hop delegation id IS the durable journal row id: the
			// ephemeral token and the persisted record share one identifier.
			DelegationID:  uuid.NewString(),
			GrantedScopes: cloneWorkScopes(scopes),
		}}
	}
	token, signed, err := s.mint(ctx, request)
	if err != nil {
		return nil, mapWorkContextError(err)
	}
	// The consumer's declaration must still be the one the ceiling was decided
	// against now that the capability is signed and before it leaves: a binding
	// withdrawn by a redelivery in between refuses it (see
	// business.ConsumerCeilingStillCurrent for what this does and does not hold).
	if consumerCeiling != nil {
		if err := service.ConsumerCeilingStillCurrent(ctx, consumerCeiling); err != nil {
			return nil, err
		}
	}
	if err := s.journalActorHop(ctx, signed); err != nil {
		return nil, err
	}
	return issuedWorkContext(token, signed), nil
}

// audienceInstallation is the installation a capability for audience in orgID is
// sealed to. Core seals every capability to one; an organization that never
// installed the module or solution an audience names has none, and nothing to
// mint the capability against.
func (s *WorkContextAuthorityServer) audienceInstallation(ctx context.Context, orgID, audience string) (string, error) {
	installation, err := s.seals.WorkContextAudienceInstallation(ctx, orgID, audience)
	switch {
	case errors.Is(err, business.ErrWorkContextNoAudienceInstallation):
		return "", status.Errorf(codes.FailedPrecondition,
			"audience %q is not installed in this organization, so there is no installation to seal the capability to", audience)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "", status.FromContextError(err).Err()
	case err != nil:
		return "", status.Error(codes.Unavailable, "cannot resolve the installation this audience names")
	}
	return installation, nil
}

// holdConfinedMintToItsConsumer bounds a mint made with a RESOURCE-CONFINED
// credential by the consumer that credential was delivered to
// (register finding SA-F-MCPAUD, round 3).
//
// Until this, nothing in this service read the confinement at all. The perimeter
// admitted a resource-bound credential at this one procedure and stamped which
// resource it named; the mint then issued for any audience in the host's
// vocabulary, any scope within the VIEWER's own authority, and any organization
// the viewer is a member of. So a credential delivered to one third-party client
// for one solution could mint a viewer-owned capability in another organization,
// addressed to a consumer that solution has nothing to do with — and the
// perimeter's own entitlement check, which asks about the credential's
// organization, could not see any of it, because the body is what names the
// organization and the audience.
//
// Three refusals, and each is a different question:
//
//  1. THE ORGANIZATION. The capability is issued for the organization the
//     credential itself names, and no other. The perimeter checked this
//     consumer's installation against THAT organization; a body naming a second
//     one would be a capability for an organization whose consent nobody asked
//     about. Membership is not consent: a person belongs to several
//     organizations and a client was delivered to one.
//
//     This binding was ALREADY enforced, incidentally: authorizeOwner's
//     membership lookup opens with RequireVerifiedDatabaseScope, which compares
//     the requested organization against the verified identity the perimeter
//     projected. So a cross-organization mint was refused before this — the
//     round-3 review's reading that it was not came from a harness whose
//     accounts was a double, and the real procedure refuses it. It is restated
//     here because the ceiling is where it belongs and because the incidental
//     refusal is about a database scope: it would survive a change to the
//     membership path that this rule must not survive, and it names something
//     other than the consumer for an operator reading the refusal.
//
//  2. THE AUDIENCE, and 3. THE SCOPES, both against what the consumer the
//     perimeter admitted under DECLARES it consumes: the invoke bindings of its
//     delivered contract, as this organization's installation consents to them
//     (business.ConsumerInvokeCeiling). The audience must be a module the
//     consumer declares, and the scopes must fit one binding it declares there.
//     A module the consumer declares that the installation did not consent to
//     — consent is fixed at install — is refused as exactly that, and so is an
//     audience naming the consumer itself.
//     The installation's `allowed_audiences` and `allowed_scopes` are then an
//     administrator's further narrowing — applied when they name something,
//     and never a source of authority, so an installation whose lists are empty
//     or wider than the declaration admits nothing the declaration does not.
//     The viewer's own authority continues to bound the mint below; a
//     capability must be inside all of them.
//
// What this does NOT re-derive is which installation the resource names. The
// perimeter resolved resource → declared binding → target → the entitlement that
// admitted this viewer, and it states the installation from that one answer.
// Asking again here would be a second resolution that could disagree with the
// one admission was decided on, and the header is the perimeter's own, stripped
// from caller input like every identity header beside it. What this DOES check is
// that the resource is one this host issues at its own origin, so a confinement
// this service cannot account for refuses rather than passing through unread.
//
// It fails CLOSED in every undecidable case: a confinement with no installation,
// an installation that cannot be resolved, a host with no configured origin. A
// capability issued on an unanswered question is the failure this whole finding
// is about.
func (s *WorkContextAuthorityServer) holdConfinedMintToItsConsumer(
	ctx context.Context,
	req *gen.StartTaskWorkContextRequest,
	permissions []business.WorkContextPermission,
	scopes []*basev0.WorkScopeV1,
) (*business.ConsumerCeiling, error) {
	resource, confined := accountsauth.VerifiedResourceConfinement(ctx)
	if !confined {
		return nil, nil
	}
	origin, configured := accountsauth.ConfiguredPublicOrigin()
	if !configured {
		// This deployment cannot say what any resource identifier IS, so it
		// cannot judge one. The perimeter refuses a confined credential in the
		// same state, so the two sides agree rather than one admitting what the
		// other cannot account for.
		return nil, status.Error(codes.FailedPrecondition,
			"this host has no configured public address, so it cannot account for the resource this credential is confined to")
	}
	if _, err := accountsauth.RequireResourceAtOrigin(resource, origin); err != nil {
		return nil, status.Error(codes.PermissionDenied,
			"the credential is confined to a resource this host does not issue")
	}
	caller, authenticated := accountsauth.VerifiedRequestIdentity(ctx)
	if !authenticated || caller.OrgID == uuid.Nil {
		return nil, status.Error(codes.PermissionDenied,
			"a confined credential names no organization, so there is none to hold this mint to")
	}
	if caller.OrgID.String() != req.GetOrgId() {
		// Neither organization is named. One is the caller's own and the other is
		// theirs to read off the request they sent; printing them would make this
		// refusal an oracle for which organizations a person belongs to.
		return nil, status.Error(codes.PermissionDenied,
			"a capability minted with a credential delivered to one consumer is issued only for that credential's own organization")
	}
	installation, admitted := accountsauth.VerifiedInstallation(ctx)
	if !admitted {
		return nil, status.Error(codes.FailedPrecondition,
			"a confined credential reached this mint without the installation it was admitted under, so there is no consented ceiling to hold it to")
	}
	// THE CEILING IS WHAT THE CONSUMER DECLARES IT CONSUMES (issue #1083).
	//
	// Not the installation's lists: those are an administrator's hand-kept
	// statement, optional, empty by default, and free to drift from what the
	// consumer actually calls — too narrow breaks it, too wide lets its
	// credential mint for modules it never uses. The consumer's delivered
	// contract names every module it calls on a person's behalf and the scopes
	// it asks for there, signed, held to the envelope and to this
	// organization's installation scope, and that is what a credential
	// delivered to it may act for. A consumer that declares no consumption of a
	// module may not be handed a capability for it, whatever the lists say.
	if service == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"this host cannot read what an installed consumer declares, so there is no ceiling to hold its credential to")
	}
	ceiling, err := service.ConsumerInvokeCeiling(ctx, req.GetOrgId(), installation)
	if err != nil {
		if _, isStatus := status.FromError(err); isStatus {
			return nil, err
		}
		return nil, status.Error(codes.Unavailable, "cannot read what the installed consumer declares")
	}
	switch ceiling.Admit(req.GetAudience(), moduleOperationScopes(scopes)) {
	case business.ConsumerCeilingAdmitted:
	case business.ConsumerCeilingOwnAudience:
		return nil, status.Errorf(codes.PermissionDenied,
			"audience %q is the consumer this credential was delivered to; the credential reaches that consumer's own routes directly, and no capability addressed to it is minted with it", req.GetAudience())
	case business.ConsumerCeilingNotConsented:
		// Consent is fixed when an organization installs, so a module the
		// consumer declared afterwards is outside it — and that, not the
		// declaration, is what an operator has to change.
		return nil, status.Errorf(codes.PermissionDenied,
			"the consumer this credential was delivered to declares this at audience %q, and the installation it was admitted under does not consent to it: "+
				"consent is fixed when the consumer is installed, and installing it again consents to what it declares now", req.GetAudience())
	case business.ConsumerCeilingOutsideDeclared:
		return nil, status.Errorf(codes.PermissionDenied,
			"the scopes asked for are outside what the consumer this credential was delivered to declares it consumes at audience %q", req.GetAudience())
	default:
		return nil, status.Errorf(codes.PermissionDenied,
			"audience %q is not a module the consumer this credential was delivered to declares it consumes", req.GetAudience())
	}

	// THE INSTALLATION'S LISTS MAY ONLY NARROW. Read through the ordinary
	// ceiling, where an empty list is no further bound — which is safe here,
	// and only here, because the declaration above has already bounded the mint.
	consumer, err := s.resolveInstallationAuthority(ctx, req.GetOrgId(), installation, permissions)
	if err != nil {
		return nil, err
	}
	if consumer.Actor == nil {
		// A healthy resolve always names the installation's agent. Fail closed
		// rather than treat a missing ceiling as an absent one: enforceActorCeiling
		// reads a nil actor as unrestricted, which is the most permissive reading
		// of the state that says least.
		return nil, status.Error(codes.Internal,
			"the consumer's installation resolved without an agent principal, so its ceiling could not be read")
	}
	if err := enforceActorCeiling(ctx, consumer.Actor, req.GetAudience(), scopes); err != nil {
		return nil, err
	}
	return ceiling, nil
}

// moduleOperationScopes is a mint's scopes in the vocabulary a consumer's
// declared bindings are written in, for a containment check. Nothing is
// normalized: a requested action or resource id the declaration does not name
// verbatim is outside it.
func moduleOperationScopes(scopes []*basev0.WorkScopeV1) []business.ModuleOperationScope {
	out := make([]business.ModuleOperationScope, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, business.ModuleOperationScope{
			ResourceKind: scope.GetResourceKind(),
			Actions:      scope.GetActions(),
			ResourceIDs:  scope.GetResourceIds(),
		})
	}
	return out
}

// StartInstallationTask is the headless mint. It opens with a service credential
// instead of a bearer and draws its authority from an installation: the owner of
// record becomes the context owner, the installation's agent principal the sole
// actor, and the agent's standing scope grants (∩ ceiling) the authority. The
// installation store resolves all of this live and fails closed on a revoked or
// disabled agent, an expired/absent standing grant, or an installation whose
// owner and co-owners have all lost org-admin status.
func (s *WorkContextAuthorityServer) StartInstallationTask(
	ctx context.Context,
	req *gen.StartInstallationTaskRequest,
) (*gen.IssuedWorkContext, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	if err := requireVocabularyAudience(ctx, req.GetAudience()); err != nil {
		return nil, err
	}
	if !s.configured() || s.authority == nil {
		return nil, status.Error(codes.FailedPrecondition, "Work Context authority is not configured")
	}
	// A headless mint names its own Task like the ordinary one, so it is held to
	// the same refusal: an installation may not name a registered solution's
	// runtime boundary (issue #1015, see taskBoundary). This path opens with a
	// service credential rather than a solution's, so it is never itself
	// solution-scoped — there is no boundary for it to be given, only one it
	// must not borrow.
	if err := s.refuseRegisteredBoundary(ctx, req.GetOrgId(), req.GetTaskId()); err != nil {
		return nil, err
	}
	permissions, scopes, err := workContextScopes(req.GetAuthorityScopes(), mintContentReads(req.GetAudience()))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	facts, err := s.resolveInstallationAuthority(
		ctx, req.GetOrgId(), req.GetInstallationId(), permissions,
	)
	if err != nil {
		return nil, err
	}
	return s.signInstallationTask(ctx, req, facts, scopes)
}

// Shared by the ordinary headless mint and the leased install step. Only the
// latter resolves pending consent; neither path changes ordinary use admission.
func (s *WorkContextAuthorityServer) signInstallationTask(ctx context.Context, req *gen.StartInstallationTaskRequest, facts *business.InstallationAuthorityFacts, scopes []*basev0.WorkScopeV1) (*gen.IssuedWorkContext, error) {
	// An installation context is always agent-actored; a nil actor means the store
	// broke its contract (a healthy resolve always names the agent). Fail closed
	// rather than mint an unattributed token.
	if facts.Actor == nil {
		return nil, status.Error(codes.Internal, "installation authority resolved without an agent actor")
	}
	// The step's kind is the issuer's to stamp on every path, not only the
	// one that stamps it: an ordinary task that could name it would carry a
	// second spelling of "run this installation's install step".
	if err := refuseInstallationStepKind(scopes); err != nil {
		return nil, err
	}
	if err := enforceActorCeiling(ctx, facts.Actor, req.GetAudience(), scopes); err != nil {
		return nil, err
	}
	return s.mintInstallationTask(ctx, req, facts, scopes)
}

// InstallationStepResourceKind and InstallationStepAction name the one scope
// every install step is delegated: to run THIS installation's install step. The
// resource id is the installation's own id, so the scope reaches nothing else.
const (
	InstallationStepResourceKind = "installations"
	InstallationStepAction       = "install"
)

// refuseInstallationStepKind refuses a caller-supplied scope that names the
// install step's kind, however it is cased or padded.
func refuseInstallationStepKind(scopes []*basev0.WorkScopeV1) error {
	for _, scope := range scopes {
		if strings.EqualFold(strings.TrimSpace(scope.GetResourceKind()), InstallationStepResourceKind) {
			return status.Errorf(codes.InvalidArgument, "an installation's scope may not name %q: the install step's own scope is the host's to add", InstallationStepResourceKind)
		}
	}
	return nil
}

// signInstallationStep signs the capability an install step is delivered
// under: the installation's own scopes, held to the agent's ceiling exactly as
// signInstallationTask holds them, plus the step's own scope.
//
// The step's scope is what keeps two rules true at once. An installation whose
// role and grant intersect to nothing is valid — the solution then acts only as
// its viewers, and its agent is delegated nothing — and it still has an install
// step to acknowledge. Core refuses a delegation hop that grants no scope,
// rightly: an empty grant is one consumer's "nothing" and another's "no
// restriction". And an installation capability is never minted unattributed.
// So the hop always grants the one thing the step is: this installation's
// install. It is added after the ceiling, which governs the kinds an agent may
// be delegated over a module's resources and has no say over the step itself.
func (s *WorkContextAuthorityServer) signInstallationStep(ctx context.Context, req *gen.StartInstallationTaskRequest, facts *business.InstallationAuthorityFacts, scopes []*basev0.WorkScopeV1) (*gen.IssuedWorkContext, error) {
	if facts.Actor == nil {
		return nil, status.Error(codes.Internal, "installation authority resolved without an agent actor")
	}
	// Before the ceiling, so the refusal names what is wrong with the scope
	// rather than what the agent may not hold.
	if err := refuseInstallationStepKind(scopes); err != nil {
		return nil, err
	}
	if err := enforceActorCeiling(ctx, facts.Actor, req.GetAudience(), scopes); err != nil {
		return nil, err
	}
	step := append(cloneWorkScopes(scopes), &basev0.WorkScopeV1{
		ResourceKind: InstallationStepResourceKind,
		Actions:      []string{InstallationStepAction},
		ResourceIds:  []string{req.GetInstallationId()},
	})
	sort.SliceStable(step, func(i, j int) bool { return step[i].GetResourceKind() < step[j].GetResourceKind() })
	return s.mintInstallationTask(ctx, req, facts, step)
}

// mintInstallationTask signs resolved, ceiling-checked installation authority.
func (s *WorkContextAuthorityServer) mintInstallationTask(ctx context.Context, req *gen.StartInstallationTaskRequest, facts *business.InstallationAuthorityFacts, scopes []*basev0.WorkScopeV1) (*gen.IssuedWorkContext, error) {
	// Sealed to the installation the authority is drawn from, owned by its owner
	// of record and actored by its agent. The session is a fresh one: this
	// caller holds a service credential, not a session, and a session id a
	// caller names is never signed (see callerSession).
	token, signed, err := s.mint(ctx, mintRequest{
		Start: workcontext.StartInput{
			TenantID:           req.GetOrgId(),
			OwnerPrincipalID:   facts.OwnerPrincipalID,
			OwnerPrincipalKind: business.PrincipalKindHuman,
			OrganizationID:     req.GetOrgId(),
			TaskID:             req.GetTaskId(),
			Audience:           req.GetAudience(),
			AuthorityScopes:    scopes,
			AttributionTeamIDs: facts.AttributionTeamIDs,
			WorkspaceID:        req.GetWorkspaceId(),
			ProjectID:          req.GetProjectId(),
			InstallationID:     req.GetInstallationId(),
		},
		Hops: []delegationHop{{
			PrincipalID:   facts.Actor.ID,
			PrincipalKind: facts.Actor.Kind,
			AgentID:       facts.Actor.AgentIdentifier,
			DelegationID:  uuid.NewString(),
			GrantedScopes: cloneWorkScopes(scopes),
		}},
		Replay: workContextReplayPolicy(req.GetReplayPolicy()),
		TTL:    workContextTTL(req.GetTtlSeconds()),
	})
	if err != nil {
		return nil, mapWorkContextError(err)
	}
	if err := s.journalActorHop(ctx, signed); err != nil {
		return nil, err
	}
	return issuedWorkContext(token, signed), nil
}

// ModuleWorkContextAudience is the one consumer a composed module's capability
// is good for. It keeps a module token and a delegated user context minted for
// another service non-interchangeable even though one key signs both.
const ModuleWorkContextAudience = "module-capabilities"

// requireMintableAudience is the audience rule EVERY mint runs, and the reason it
// is one function rather than a line repeated at each site.
//
// Two refusals, each naming what it refused:
//
//  1. EMPTY. An audience is what makes two capabilities signed by one key
//     non-interchangeable, so a token stamped with none is good at whichever
//     consumer will take it. It was legal on every caller-supplied mint, and the
//     verifier could not make up the difference: the expectation check treats an
//     empty EXPECTED audience as "do not check" (sdk-go
//     workcontext/work_context.go:570, `check.want != ""`), so a site with no
//     value to pass and a token with no value to compare agreed, vacuously. The
//     host's answer is to stamp a value always, which is what makes the sentinel
//     unreachable from here — see the PR body for the upstream half, which is
//     sdk-go's to delete and not this repo's to patch.
//
//  2. The capability surface's OWN audience. A token addressed to
//     "module-capabilities" is what that surface accepts as a MODULE'S IDENTITY
//     (VerifyModuleWorkContext expects exactly this audience and then reads the
//     principal out of the actor chain). A caller-supplied mint that could name
//     it would hand any owner a second spelling of a module identity — sealed
//     with scopes that surface never reads. StartModuleTask stamps it itself;
//     nothing a caller asks for may.
//
// These two are STRUCTURAL and need no state, which is why they are separate from
// the vocabulary check below: they hold even when the registry cannot be read, so a
// registry outage never turns into a mint that stamps an empty audience.
func requireMintableAudience(audience string) error {
	if strings.TrimSpace(audience) == "" {
		return status.Error(codes.InvalidArgument,
			"a Work Context audience is required: a capability with no audience is good at whichever consumer accepts it")
	}
	if audience == ModuleWorkContextAudience {
		return status.Errorf(codes.PermissionDenied,
			"audience %q is this host's module capability surface and is never caller-supplied: a context addressed to it is read as a module identity",
			ModuleWorkContextAudience)
	}
	return nil
}

// requireVocabularyAudience is the second half: the audience must be in the host's
// CLOSED, DERIVED set — the module capability audience, or
// `solution:<binding-id>` for a declared, non-tombstoned binding, and nothing
// else (business.RequireHostAudience).
//
// Both halves run, in this order, and neither subsumes the other. The structural
// half needs no state and therefore holds during a registry outage. The
// vocabulary half needs the declared registry and FAILS CLOSED when it cannot be
// read: a set that cannot be resolved must not read as "no audiences are valid"
// *or* as "any is", so the mint refuses and says which capability the deployment
// is missing.
//
// The set is derived from delivered presence, so it changes only by delivery. A
// caller never adds to it by asking, and a withdrawal removes an audience with its
// binding's tombstone — which is what makes "the consumer this capability names
// still exists" a property of the mint rather than of a reviewer's attention.
func requireVocabularyAudience(ctx context.Context, audience string) error {
	if err := requireMintableAudience(audience); err != nil {
		return err
	}
	if err := service.RequireHostAudience(ctx, audience); err != nil {
		if errors.Is(err, business.ErrAudienceNotInHostVocabulary) {
			return status.Error(codes.PermissionDenied, err.Error())
		}
		// The set could not be resolved. Unavailable, not PermissionDenied: the
		// caller may be entitled and nothing here says otherwise, and an operator
		// reading a 503 looks at the registry rather than at a grant.
		return status.Error(codes.Unavailable, err.Error())
	}
	return nil
}

// ErrWorkContextAuthorityUnconfigured distinguishes a deployment that never
// wired the signing key from a capability this issuer refused to sign. Both
// reach the caller as a failure to mint, but only one is the operator's to fix.
var ErrWorkContextAuthorityUnconfigured = errors.New("Work Context authority is not configured")

// moduleWorkContextTTL is the window of a module's own identity capability. A
// module re-mints at expiry by presenting its projected token again, so this is
// how long a superseded pod could hold one at most, not a session length.
const moduleWorkContextTTL = 15 * time.Minute

// StartModuleTask mints the capability a composed module presents to the
// module-facing surface, owned by the module's service principal and acting as
// it directly.
//
// It is sealed to the installation the business mint resolved, the principal's
// producer epoch — so a revoked installation or an advanced epoch refuses the
// capability at its next use rather than at its expiry. It seals no build: what
// a workload runs is deploy-time admission, and the mint bound the caller to a
// workload its applied manifest lists. Every use still re-reads
// and intersects the live installation under an authority fence.
//
// This method only signs already-resolved authority. The business layer owns
// execution binding, live consent, tenant validation and the issuance audit.
func (s *WorkContextAuthorityServer) StartModuleTask(
	ctx context.Context, authority business.ModuleWorkContextAuthority,
) (string, *basev0.WorkContextV1, error) {
	return s.mint(ctx, mintRequest{
		Start: workcontext.StartInput{
			TenantID:           authority.Tenant,
			OwnerPrincipalID:   authority.PrincipalID,
			OwnerPrincipalKind: business.PrincipalKindService,
			OrganizationID:     authority.Tenant,
			TaskID:             uuid.NewString(),
			Audience:           ModuleWorkContextAudience,
			InstallationID:     authority.InstallationID,
		},
		Replay: workcontext.ReplayIdempotent,
		TTL:    moduleWorkContextTTL,
	})
}

// StartModuleOperationTask mints the capability a composed module presents, with
// no person present, to the service behind one of its installed operation
// audiences. It is owned by the module's service principal acting directly, the
// tenant is the one its principal declares, and the authority is exactly the
// binding's headless scopes — sealed here, unlike StartModuleTask, because the
// audience is another service that decides from the token alone. The lifetime is
// the short operation-context ceiling rather than the Work Context maximum.
//
// As with StartModuleTask no actor hop is journaled: a module principal is
// declared by the deployment, not registered, and the durable record is the
// issuance audit event.
func (s *WorkContextAuthorityServer) StartModuleOperationTask(
	ctx context.Context, authority business.ModuleOperationContextAuthority,
) (string, *basev0.WorkContextV1, error) {
	// The same rule every mint runs, stated once: an empty audience, and the
	// capability surface's own, are both refused. The error KIND stays this
	// path's own (core's ErrInvalid), because its callers map it.
	if requireMintableAudience(authority.Audience) != nil {
		return "", nil, fmt.Errorf("%w: operation context audience", workcontext.ErrInvalid)
	}
	if authority.Revision == 0 {
		return "", nil, fmt.Errorf("%w: operation context revision", workcontext.ErrInvalid)
	}
	_, scopes, err := workContextScopes(authority.WireScopes(), mintContentReads(authority.Audience))
	if err != nil || len(scopes) == 0 {
		return "", nil, fmt.Errorf("%w: operation context scopes", workcontext.ErrInvalid)
	}
	// The authorization revision is not an input: core reads it, from the same
	// answer CheckAuthorizationRevision confirms a consumer's copy against
	// (ModuleOperationContextRevision of the installed grant).
	return s.mint(ctx, mintRequest{
		Start: workcontext.StartInput{
			TenantID:           authority.Tenant,
			OwnerPrincipalID:   authority.PrincipalID,
			OwnerPrincipalKind: business.PrincipalKindService,
			OrganizationID:     authority.Tenant,
			TaskID:             uuid.NewString(),
			Audience:           authority.Audience,
			AuthorityScopes:    scopes,
			InstallationID:     authority.InstallationID,
		},
		Replay: workcontext.ReplayIdempotent,
		TTL:    business.ModuleOperationContextTTL,
	})
}

// VerifyModuleWorkContext authenticates a module's identity capability and
// returns the principal exercising it, the tenant it is bound to, and what it
// was sealed to — so every capability use compares the live installation against
// the one the credential CLAIMS (ModuleCapabilityAuthority.checkAgainstSealed),
// not only against itself.
func (s *WorkContextAuthorityServer) VerifyModuleWorkContext(ctx context.Context, encoded string) (business.ModuleCaller, error) {
	if !s.configured() {
		return business.ModuleCaller{}, status.Error(codes.FailedPrecondition, "Work Context authority is not configured")
	}
	verified, err := s.verifyFor(ctx, encoded, ModuleWorkContextAudience)
	if err != nil {
		return business.ModuleCaller{}, status.Error(codes.Unauthenticated, "module work context is not a valid capability")
	}
	claims := verified.Context()
	principal := claims.GetOwnerPrincipalId()
	if actor := verified.Actor(); actor != nil {
		principal = actor.GetPrincipalId()
	}
	seal := claims.GetSeal()
	installation, revision, epoch := seal.GetInstallationId(), seal.GetInstallationRevision(), seal.GetPrincipalEpoch()
	sealed := &business.SealedModuleAuthority{
		InstallationID:       &installation,
		InstallationRevision: &revision,
		ProducerEpoch:        &epoch,
	}
	return business.ModuleCaller{
		PrincipalID: principal,
		BoundOrg:    claims.GetTenantId(),
		Sealed:      sealed,
	}, nil
}

func (s *WorkContextAuthorityServer) resolveInstallationAuthority(
	ctx context.Context,
	orgID string,
	installationID string,
	permissions []business.WorkContextPermission,
) (*business.InstallationAuthorityFacts, error) {
	facts, err := s.authority.ResolveInstallationAuthority(ctx, orgID, installationID, permissions)
	if err == nil {
		return facts, nil
	}
	var storeErr *business.StoreError
	if errors.As(err, &storeErr) {
		switch storeErr.StoreErrorType {
		case business.ErrTypeNotFound:
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		case business.ErrTypePermission:
			return nil, status.Error(codes.PermissionDenied, err.Error())
		}
	}
	return nil, status.Error(codes.Internal, "cannot resolve installation authority")
}

// StartRootSession issues another root Session under the same Task. The Session
// id is assigned here rather than taken from the request: a root Session keeps
// no lineage, so a caller-chosen id would be free to name anything at all —
// including a Session that never existed or another credential's. The issued
// response carries the id, which is what a caller correlates on and what the
// product records its own Session row under.
//
// The capability is minted afresh from its owner with the parent's chain of
// hops re-derived against live state, which is how core expresses re-issuing a
// capability (see remint).
func (s *WorkContextAuthorityServer) StartRootSession(
	ctx context.Context,
	req *gen.StartRootSessionWorkContextRequest,
) (*gen.IssuedWorkContext, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireVocabularyAudience(ctx, req.GetAudience()); err != nil {
		return nil, err
	}
	ownerID, err := s.authorizeOwner(ctx, req.GetOrgId())
	if err != nil {
		return nil, err
	}
	parent, actor, err := s.verifyParent(ctx, req.GetOrgId(), ownerID, req.GetParentWorkContextToken())
	if err != nil {
		return nil, err
	}
	if err := enforceActorAudience(ctx, actor, req.GetAudience()); err != nil {
		return nil, err
	}
	token, signed, err := s.remint(ctx, parent.Context(), req.GetAudience(),
		workContextReplayPolicy(req.GetReplayPolicy()), workContextTTL(req.GetTtlSeconds()),
		nil, "", moduleBindingSeal{})
	if err != nil {
		return nil, mapWorkContextError(err)
	}
	return issuedWorkContext(token, signed), nil
}

func (s *WorkContextAuthorityServer) ExchangeAudience(
	ctx context.Context,
	req *gen.ExchangeWorkContextAudienceRequest,
) (*gen.IssuedWorkContext, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireVocabularyAudience(ctx, req.GetAudience()); err != nil {
		return nil, err
	}
	ownerID, err := s.authorizeOwner(ctx, req.GetOrgId())
	if err != nil {
		return nil, err
	}
	parent, actor, err := s.verifyParent(ctx, req.GetOrgId(), ownerID, req.GetParentWorkContextToken())
	if err != nil {
		return nil, err
	}
	// The owner is exchanging their own capability, so it is re-issued in the
	// session they authenticated with.
	return s.exchangeVerifiedParent(ctx, parent.Context(), actor, req, callerSession(ctx), moduleBindingSeal{})
}

// exchangeVerifiedParent runs AFTER the caller's authentication and
// current-parent checks. session is the issuer-assigned session the exchanged
// capability opens ("" for a fresh one).
func (s *WorkContextAuthorityServer) exchangeVerifiedParent(
	ctx context.Context, parent *basev0.WorkContextV1, actor *business.Principal,
	req *gen.ExchangeWorkContextAudienceRequest, session string, exercised moduleBindingSeal,
) (*gen.IssuedWorkContext, error) {
	if req.GetAudience() == parent.GetAudience() {
		return nil, status.Error(codes.InvalidArgument, "new audience must differ from parent audience")
	}
	_, scopes, err := workContextScopes(req.GetAttenuatedScopes(), mintContentReads(req.GetAudience()))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	// Enforce both dimensions of the actor ceiling: an exchange can name a new
	// audience AND re-declare scopes, so the scope cap must apply here too, not
	// only the audience.
	if err := enforceActorCeiling(ctx, actor, req.GetAudience(), scopes); err != nil {
		return nil, err
	}
	attenuated, err := attenuateEffective(parent, scopes)
	if err != nil {
		return nil, err
	}
	token, signed, err := s.remint(ctx, parent, req.GetAudience(),
		workContextReplayPolicy(req.GetReplayPolicy()), workContextTTL(req.GetTtlSeconds()),
		attenuated, session, exercised)
	if err != nil {
		return nil, mapWorkContextError(err)
	}
	return issuedWorkContext(token, signed), nil
}

// attenuateEffective checks that requested scopes narrow the capability's
// effective authority — the last hop's granted scopes, or the owner's when it
// acts directly — and returns them, or nil when none were requested (the chain
// is re-issued as it stands). Widening is refused here, before anything is
// minted, rather than left for core's derivation to refuse with less to say.
// attenuateEffective bounds requested — an installed binding's or a renewal's
// declared scopes, which are policy CEILINGS — by what the parent actually
// carries. A ceiling that names no resource ids over a parent narrowed to some
// keeps the parent's ids: a module that minted its parent at the least
// authority it needs (one model profile, one collection) is not refused for
// having been careful. A module did exactly that — a parent of a read on one
// profile, redeemed through a binding whose lookup ceiling is the kind — and
// every turn was refused "widens authority" before the first model call. Only
// a kind, an action or an id the parent never
// carried is a widening, and that stays refused by name.
func attenuateEffective(parent *basev0.WorkContextV1, requested []*basev0.WorkScopeV1) ([]*basev0.WorkScopeV1, error) {
	if len(requested) == 0 {
		return nil, nil
	}
	effective := parent.GetAuthorityScopes()
	if chain := parent.GetActorChain(); len(chain) > 0 {
		effective = chain[len(chain)-1].GetGrantedScopes()
	}
	attenuated := make([]*basev0.WorkScopeV1, 0, len(requested))
	for _, scope := range requested {
		if workcontext.ScopeContained(scope, effective) {
			attenuated = append(attenuated, scope)
			continue
		}
		narrowed := narrowToParentIDs(scope, effective)
		if narrowed == nil || !workcontext.ScopeContained(narrowed, effective) {
			return nil, status.Errorf(codes.InvalidArgument, "audience exchange widens authority: the parent does not carry %s:%s", scope.GetResourceKind(), strings.Join(scope.GetActions(), ","))
		}
		attenuated = append(attenuated, narrowed)
	}
	return attenuated, nil
}

// narrowToParentIDs is the one narrowing attenuation performs itself: a
// kind-wide ceiling takes the resource ids the parent holds for that kind. It
// reads the parent the way core's ScopeContained does — the first scope of the
// kind — so what it returns is exactly what that check then verifies. nil when
// the ceiling already names ids or the parent holds the kind unrestricted or
// not at all.
func narrowToParentIDs(scope *basev0.WorkScopeV1, effective []*basev0.WorkScopeV1) *basev0.WorkScopeV1 {
	if len(scope.GetResourceIds()) != 0 {
		return nil
	}
	for _, held := range effective {
		if held.GetResourceKind() != scope.GetResourceKind() {
			continue
		}
		if len(held.GetResourceIds()) == 0 {
			return nil
		}
		narrowed := proto.Clone(scope).(*basev0.WorkScopeV1)
		narrowed.ResourceIds = slices.Clone(held.GetResourceIds())
		return narrowed
	}
	return nil
}

// StartChildSession delegates one more hop from a verified parent. The child's
// session is assigned by this issuer and returned in the response, never taken
// from the request: a session a caller could name is a session it could name
// after another credential's (see callerSession).
func (s *WorkContextAuthorityServer) StartChildSession(
	ctx context.Context,
	req *gen.StartChildSessionWorkContextRequest,
) (*gen.IssuedWorkContext, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireVocabularyAudience(ctx, req.GetAudience()); err != nil {
		return nil, err
	}
	ownerID, err := s.authorizeOwner(ctx, req.GetOrgId())
	if err != nil {
		return nil, err
	}
	parent, _, err := s.verifyParent(ctx, req.GetOrgId(), ownerID, req.GetParentWorkContextToken())
	if err != nil {
		return nil, err
	}
	if err := s.requireChildKeepsAudienceSeal(ctx, parent.Context(), req.GetAudience()); err != nil {
		return nil, err
	}
	permissions, scopes, err := workContextScopes(req.GetGrantedScopes(), mintContentReads(req.GetAudience()))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	facts, err := s.resolveAuthority(
		ctx, req.GetOrgId(), ownerID, req.GetActorPrincipalId(), permissions,
	)
	if err != nil {
		return nil, err
	}
	if err := enforceActorCeiling(ctx, facts.Actor, req.GetAudience(), scopes); err != nil {
		return nil, err
	}
	ttl := workContextTTL(req.GetTtlSeconds())
	if ttl == 0 {
		ttl = workContextDefaultTTL
	}
	// Core's Child holds the parent against the live revision and seal before it
	// derives, so a parent minted before an authorization change derives nothing.
	token, signed, err := s.coreAuthority(req.GetOrgId(), ownerID).Child(ctx, parent, workcontext.ChildInput{
		PrincipalID:   facts.Actor.ID,
		PrincipalKind: facts.Actor.Kind,
		AgentID:       facts.Actor.AgentIdentifier,
		DelegationID:  uuid.NewString(),
		GrantedScopes: scopes,
		Audience:      req.GetAudience(),
		ReplayPolicy:  workContextReplayPolicy(req.GetReplayPolicy()),
		TTL:           ttl,
	})
	if err != nil {
		return nil, mapWorkContextError(err)
	}
	if err := s.journalActorHop(ctx, signed); err != nil {
		return nil, err
	}
	return issuedWorkContext(token, signed), nil
}

// requireChildKeepsAudienceSeal refuses a child of a PERSON's capability that
// would carry another installation's seal.
//
// The invariant: a capability owned by a person is sealed to the organization's
// installation of the audience it addresses. Every other person mint re-derives
// the seal from the audience (StartTask, and remint for StartRootSession,
// ExchangeAudience and RenewWorkContext). Core's Child cannot: a child keeps its
// parent's seal, so a child for another audience would be a person's capability
// sealed to an installation that is not its audience's. The gateway's bearer-less
// module arm reads exactly that difference as "unattended work" (a module or
// installation principal is sealed to its own installation), so such a child
// would be admitted there without the person's bearer.
//
// A same-audience child keeps the seal its audience resolves to now, and is
// minted. A principal that is not a person is sealed to its own installation,
// which the audience does not change, and is not held here.
// TestAPersonsCapabilityIsSealedToItsAudiencesInstallation pins the invariant
// over every person mint.
func (s *WorkContextAuthorityServer) requireChildKeepsAudienceSeal(
	ctx context.Context, parent *basev0.WorkContextV1, audience string,
) error {
	if parent.GetOwnerPrincipalKind() != business.PrincipalKindHuman {
		return nil
	}
	installation, err := s.audienceInstallation(ctx, parent.GetTenantId(), audience)
	if err != nil {
		return err
	}
	if installation != parent.GetSeal().GetInstallationId() {
		return status.Error(codes.PermissionDenied,
			"a child of a person's Work Context keeps its parent's installation, so it may address only an audience sealed to that installation")
	}
	return nil
}

// RenewWorkContext lets the current actor of a delegated context re-issue it
// with a fresh window while the originating user is offline. It keeps the Task,
// the owner and every delegation hop, appends none, and opens a fresh Session;
// it only refreshes the lifetime and, optionally, the audience. Renewed
// authority is bounded by the current actor's granted scopes and can only be
// attenuated, so it can never widen the original grant.
//
// Core never extends a capability's window, so a renewal is a re-mint from the
// owner with every hop re-derived against live state (see remint), never a copy
// of the parent with a later expiry.
func (s *WorkContextAuthorityServer) RenewWorkContext(
	ctx context.Context,
	req *gen.RenewWorkContextRequest,
) (*gen.IssuedWorkContext, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if !s.configured() || s.authority == nil {
		return nil, status.Error(codes.FailedPrecondition, "Work Context authority is not configured")
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.GetOrgId()); err != nil {
		return nil, err
	}
	verified, actor, err := s.verifyActorParent(ctx, req.GetOrgId(), actorID, req.GetParentWorkContextToken())
	if err != nil {
		return nil, err
	}
	parent := verified.Context()
	var scopes []*basev0.WorkScopeV1
	if len(req.GetAttenuatedScopes()) > 0 {
		// Only the validated wire scopes are wanted here: a renewal draws its
		// authority from requireCurrentAuthority on the parent, not from a fresh
		// permission resolution, so the permission set is discarded and the
		// content-read rule never reaches a decision.
		if _, scopes, err = workContextScopes(req.GetAttenuatedScopes(), noContentReads); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}
	// The tri-state made explicit: a renewal that names no audience INHERITS the
	// parent's by copying it here, and the resolved value — never the request
	// field — is what is checked and what is stamped. A parent that somehow
	// carries no audience therefore refuses the renewal instead of propagating
	// the emptiness one generation further.
	audience := req.GetAudience()
	if audience == "" {
		audience = parent.GetAudience()
	}
	if err := requireVocabularyAudience(ctx, audience); err != nil {
		return nil, err
	}
	// A renewal may re-declare attenuated scopes, so enforce both dimensions of
	// the actor ceiling — audience and resource kinds — not the audience alone.
	if err := enforceActorCeiling(ctx, actor, audience, scopes); err != nil {
		return nil, err
	}
	attenuated, err := attenuateEffective(parent, scopes)
	if err != nil {
		return nil, err
	}
	// A renewal must not silently loosen replay protection: an unspecified
	// policy inherits the parent's, so a SINGLE_USE context stays SINGLE_USE
	// rather than degrading to idempotent.
	replayPolicy := parent.GetReplayPolicy()
	if req.GetReplayPolicy() != gen.WorkContextReplayPolicy_WORK_CONTEXT_REPLAY_POLICY_UNSPECIFIED {
		replayPolicy = workContextReplayPolicy(req.GetReplayPolicy())
	}
	token, signed, err := s.remint(ctx, parent, audience, replayPolicy, workContextTTL(req.GetTtlSeconds()),
		attenuated, "", moduleBindingSeal{})
	if err != nil {
		return nil, mapWorkContextError(err)
	}
	return issuedWorkContext(token, signed), nil
}

// journalActorHop durably records the newest on-behalf-of hop of a freshly
// signed Work Context, content-addressed and chained to the hop it narrows from.
// The hop id equals the token's per-hop delegation_id, so the durable record and
// the ephemeral token share one identifier. Durability is a guarantee, not
// best-effort: a failed append fails issuance rather than minting an
// unaccountable token.
func (s *WorkContextAuthorityServer) journalActorHop(
	ctx context.Context,
	signed *basev0.WorkContextV1,
) error {
	if s.journal == nil {
		return nil
	}
	chain := signed.GetActorChain()
	if len(chain) == 0 {
		return nil
	}
	index := len(chain) - 1
	actor := chain[index]
	parentDelegationID := ""
	if index > 0 {
		parentDelegationID = chain[index-1].GetDelegationId()
	}
	_, err := s.journal.AppendActorChainHop(ctx, business.ActorChainHopInput{
		ID:                    actor.GetDelegationId(),
		OrgID:                 signed.GetTenantId(),
		TaskID:                signed.GetTaskId(),
		SessionID:             signed.GetSessionId(),
		OwnerPrincipalID:      signed.GetOwnerPrincipalId(),
		ActorPrincipalID:      actor.GetPrincipalId(),
		ActorKind:             actor.GetPrincipalKind(),
		ParentDelegationID:    parentDelegationID,
		GrantedScopes:         actorChainScopes(actor.GetGrantedScopes()),
		AuthorizationRevision: signed.GetAuthorizationRevision(),
		HopIndex:              index,
	})
	if err != nil {
		return status.Error(codes.Internal, "cannot persist actor chain hop")
	}
	return nil
}

// requireChainNotRevoked fails closed if any hop in the parent chain, or any
// ancestor those hops chain through, has been revoked. This is the per-hop
// revocation layer that stops a revoked branch from deriving new tokens; the
// revocation also bumps the owner/org authorization revision (see migration 99)
// so already-issued tokens on that branch are rejected on the action path too.
// The whole chain is checked in one query. A hop with no journal record is
// treated as live: journalActorHop fails issuance on a failed append, so no
// token issued after durability shipped can lack its hop; the only unjournaled
// hops predate the journal and cannot have been revoked (revocation needs a row).
func (s *WorkContextAuthorityServer) requireChainNotRevoked(
	ctx context.Context,
	orgID string,
	actors []*basev0.WorkActorV1,
) error {
	if s.journal == nil {
		return nil
	}
	delegationIDs := make([]string, 0, len(actors))
	for _, actor := range actors {
		if id := actor.GetDelegationId(); id != "" {
			delegationIDs = append(delegationIDs, id)
		}
	}
	if len(delegationIDs) == 0 {
		return nil
	}
	revoked, err := s.journal.AnyActorChainHopRevoked(ctx, orgID, delegationIDs)
	if err != nil {
		return status.Error(codes.Internal, "cannot check actor chain revocation")
	}
	if revoked {
		return status.Error(codes.PermissionDenied, "actor chain hop has been revoked")
	}
	return nil
}

func actorChainScopes(scopes []*basev0.WorkScopeV1) []business.ActorChainScope {
	out := make([]business.ActorChainScope, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, business.ActorChainScope{
			ResourceKind: scope.GetResourceKind(),
			Actions:      append([]string(nil), scope.GetActions()...),
			ResourceIDs:  append([]string(nil), scope.GetResourceIds()...),
		})
	}
	return out
}

func (s *WorkContextAuthorityServer) authorizeOwner(ctx context.Context, orgID string) (string, error) {
	if !s.configured() || s.authority == nil {
		return "", status.Error(codes.FailedPrecondition, "Work Context authority is not configured")
	}
	ownerID, err := requireAuth(ctx)
	if err != nil {
		return "", err
	}
	if err := requireOrgMember(ctx, ownerID, orgID); err != nil {
		return "", err
	}
	return ownerID, nil
}

// callerSession is the session a capability the caller opens is bound to: the
// session the caller authenticated with, or "" — which core turns into a fresh
// one — when it has none. A request cannot name one: StartTask has no session
// field (it is reserved), like every other mint.
//
// That is what makes a session id identify its owner. A party holding a
// confined access token pairs a capability with it on the session alone (the
// gateway's module routes do), and the pairing holds only while every session a
// capability carries is either its caller's verified session or a fresh one.
// The issued response carries the session actually bound, which is what a
// caller correlates on.
func callerSession(ctx context.Context) string {
	verified, ok := accountsauth.VerifiedSessionID(ctx)
	return issuedSession(verified, ok)
}

// verifyParent verifies a parent capability its OWNER presents to derive from:
// this issuer's own capability (verifyIssued), bound to the organization and to
// the caller as owner, and still backed by current authority.
func (s *WorkContextAuthorityServer) verifyParent(
	ctx context.Context,
	orgID string,
	ownerID string,
	encoded string,
) (*workcontext.Verified, *business.Principal, error) {
	parent, err := s.verifyIssued(ctx, encoded)
	if err != nil {
		return nil, nil, refusedReadBack(ctx, err, status.Error(codes.PermissionDenied, "parent Work Context is not valid for caller"))
	}
	claims := parent.Context()
	if claims.GetTenantId() != orgID || claims.GetOwnerPrincipalId() != ownerID {
		return nil, nil, status.Error(codes.PermissionDenied, "parent Work Context is not valid for caller")
	}
	actor, err := s.requireCurrentAuthority(ctx, orgID, claims)
	if err != nil {
		return nil, nil, err
	}
	return parent, actor, nil
}

// verifyActorParent is the actor-authorized counterpart of verifyParent. The
// token is bound to the org and issuer but NOT to the caller as owner: the
// caller must instead be the current (outermost) actor of a delegated chain, so
// an in-flight actor can renew its own authority without the owner present. A
// direct owner-only context has no actor chain and cannot be renewed this way.
func (s *WorkContextAuthorityServer) verifyActorParent(
	ctx context.Context,
	orgID string,
	actorID string,
	encoded string,
) (*workcontext.Verified, *business.Principal, error) {
	parent, err := s.verifyIssued(ctx, encoded)
	if err != nil {
		return nil, nil, refusedReadBack(ctx, err, status.Error(codes.PermissionDenied, "parent Work Context is not valid"))
	}
	if parent.Context().GetTenantId() != orgID {
		return nil, nil, status.Error(codes.PermissionDenied, "parent Work Context is not valid")
	}
	claims := parent.Context()
	actors := claims.GetActorChain()
	if len(actors) == 0 {
		return nil, nil, status.Error(
			codes.PermissionDenied,
			"only a delegated actor may renew a Work Context",
		)
	}
	if actors[len(actors)-1].GetPrincipalId() != actorID {
		return nil, nil, status.Error(
			codes.PermissionDenied,
			"caller is not the current actor of this Work Context",
		)
	}
	actor, err := s.requireCurrentAuthority(ctx, orgID, claims)
	if err != nil {
		return nil, nil, err
	}
	return parent, actor, nil
}

// requireCurrentAuthority fails closed unless the parent's sealed authorization
// revision still matches every subject's current RBAC authority and no chain
// hop has been revoked. The owner and actor exchange paths share it so a
// renewal is subject to the same revocation checks as an owner-driven exchange.
// It returns the current (outermost) delegated actor it resolved — nil for an
// owner-only context — so derive callers can gate the requested audience against
// that actor's ceiling without a second authority resolution.
func (s *WorkContextAuthorityServer) requireCurrentAuthority(
	ctx context.Context,
	orgID string,
	parent *basev0.WorkContextV1,
) (*business.Principal, error) {
	ownerID := parent.GetOwnerPrincipalId()
	if len(parent.GetActorChain()) == 0 {
		permissions := permissionsFromCoreScopes(parent.GetAuthorityScopes(), recheckContentReads())
		facts, err := s.resolveAuthority(ctx, orgID, ownerID, "", permissions)
		if err != nil {
			return nil, err
		}
		if facts.EffectiveRevision() != parent.GetAuthorizationRevision() {
			return nil, status.Error(
				codes.FailedPrecondition,
				"parent Work Context authorization revision is stale",
			)
		}
		return nil, nil
	}
	if err := s.requireChainNotRevoked(ctx, orgID, parent.GetActorChain()); err != nil {
		return nil, err
	}
	var outermostActor *business.Principal
	for _, actor := range parent.GetActorChain() {
		permissions := permissionsFromCoreScopes(actor.GetGrantedScopes(), recheckContentReads())
		facts, err := s.resolveAuthority(
			ctx, orgID, ownerID, actor.GetPrincipalId(), permissions,
		)
		if err != nil {
			return nil, err
		}
		if facts.EffectiveRevision() != parent.GetAuthorizationRevision() {
			return nil, status.Error(
				codes.FailedPrecondition,
				"parent Work Context authorization revision is stale",
			)
		}
		outermostActor = facts.Actor
	}
	return outermostActor, nil
}

func (s *WorkContextAuthorityServer) resolveAuthority(
	ctx context.Context,
	orgID string,
	ownerID string,
	actorID string,
	permissions []business.WorkContextPermission,
) (*business.WorkContextAuthorityFacts, error) {
	facts, err := s.authority.ResolveWorkContextAuthority(
		ctx, orgID, ownerID, actorID, permissions,
	)
	if err == nil {
		if actorID != "" && facts.Actor == nil {
			return nil, status.Error(codes.Internal, "authority store omitted requested actor")
		}
		return facts, nil
	}
	var storeErr *business.StoreError
	if errors.As(err, &storeErr) {
		switch storeErr.StoreErrorType {
		case business.ErrTypeNotFound:
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		case business.ErrTypePermission:
			return nil, status.Error(codes.PermissionDenied, err.Error())
		}
	}
	return nil, status.Error(codes.Internal, "cannot resolve current Work Context authority")
}

// enforceActorCeiling rejects a mint whose requested audience or scopes fall
// outside the delegated actor's registered ceiling (CreateAgentPrincipal's
// allowed_audiences / allowed_scopes). An empty ceiling list is unrestricted,
// and a nil actor is an owner-only (non-delegated) context that carries none.
//
// The scope ceiling is deliberately at resource-KIND granularity, not
// kind+action: per-action authority is already granted through role_assignments
// (the RBAC check resolveAuthority runs), so a finer ceiling would duplicate
// that. allowed_scopes is the coarser, non-redundant "which resource kinds may
// this agent ever touch via a Work Context" cap that RBAC does not express.
func enforceActorCeiling(
	ctx context.Context, actor *business.Principal, audience string, scopes []*basev0.WorkScopeV1,
) error {
	if err := enforceActorAudience(ctx, actor, audience); err != nil {
		return err
	}
	if actor == nil || len(actor.AllowedScopes) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(actor.AllowedScopes))
	for _, kind := range actor.AllowedScopes {
		allowed[kind] = struct{}{}
	}
	for _, scope := range scopes {
		if _, ok := allowed[scope.GetResourceKind()]; !ok {
			return status.Errorf(codes.PermissionDenied,
				"resource kind %q is outside agent %s allowed scopes",
				scope.GetResourceKind(), actor.AgentIdentifier)
		}
	}
	return nil
}

// enforceActorAudience holds a mint to the actor's registered ceiling, and the
// ceiling is read through the host's CURRENT vocabulary (issue #952).
//
// A stored entry that has left the closed set is DEAD, NOT GRANTED. A solution
// withdrawn after an installation named its audience leaves a ceiling entry that
// no longer describes any consumer this host serves; granting it would mint a
// capability for an audience delivery has taken away. Write-time validation
// cannot cover this on its own, because the set shrinks by DELIVERY, long after
// any write — so the narrowing happens here, on the read.
//
// A ceiling narrowed to NOTHING refuses, and that is the one case where this is
// not merely a filter: an empty stored list means "unrestricted", but a list that
// was non-empty and is now entirely dead means "every consumer this agent was
// allowed to address is gone". Collapsing the second into the first would turn a
// fully-withdrawn ceiling into no ceiling at all, which is the most permissive
// reading of the most restrictive state.
func enforceActorAudience(ctx context.Context, actor *business.Principal, audience string) error {
	if actor == nil || len(actor.AllowedAudiences) == 0 {
		return nil
	}
	live, err := service.LiveHostAudiences(ctx, actor.AllowedAudiences)
	if err != nil {
		// Fail closed: a ceiling that cannot be read is not an unrestricted one.
		return status.Error(codes.Unavailable, err.Error())
	}
	if len(live) == 0 {
		return status.Errorf(codes.PermissionDenied,
			"agent %s has %d allowed audience(s) and this host serves none of them any more: the solutions they named have been withdrawn",
			actor.AgentIdentifier, len(actor.AllowedAudiences))
	}
	for _, allowed := range live {
		if allowed == audience {
			return nil
		}
	}
	return status.Errorf(codes.PermissionDenied,
		"audience %q is outside agent %s allowed audiences this host still serves", audience, actor.AgentIdentifier)
}

func workContextScopes(
	input []*gen.WorkContextScope,
	eligible contentReadEligible,
) ([]business.WorkContextPermission, []*basev0.WorkScopeV1, error) {
	permissions := make([]business.WorkContextPermission, 0)
	scopes := make([]*basev0.WorkScopeV1, 0, len(input))
	seenScopes := make(map[string]struct{}, len(input))
	for _, scope := range input {
		if scope == nil {
			return nil, nil, errors.New("authority scope must not be null")
		}
		resourceKind := strings.TrimSpace(scope.GetResourceKind())
		if resourceKind != scope.GetResourceKind() {
			return nil, nil, errors.New("resource_kind must not have surrounding whitespace")
		}
		coreScope := &basev0.WorkScopeV1{
			ResourceKind: resourceKind,
			Actions:      append([]string(nil), scope.GetActions()...),
			ResourceIds:  append([]string(nil), scope.GetResourceIds()...),
		}
		scopeKey := resourceKind + "\x00" + strings.Join(coreScope.Actions, "\x00") +
			"\x01" + strings.Join(coreScope.ResourceIds, "\x00")
		if _, exists := seenScopes[scopeKey]; exists {
			return nil, nil, errors.New("duplicate authority scope")
		}
		seenScopes[scopeKey] = struct{}{}

		for _, action := range coreScope.Actions {
			if action != strings.TrimSpace(action) {
				return nil, nil, errors.New("scope action must not have surrounding whitespace")
			}
			if len(coreScope.ResourceIds) == 0 {
				permissions = append(permissions, business.WorkContextPermission{
					ResourceKind: resourceKind,
					Action:       action,
				})
				continue
			}
			for _, resourceID := range coreScope.ResourceIds {
				if resourceID != strings.TrimSpace(resourceID) {
					return nil, nil, errors.New("resource_id must not have surrounding whitespace")
				}
				permissions = append(permissions, business.WorkContextPermission{
					ResourceKind: resourceKind,
					Action:       action,
					ResourceID:   resourceID,
				})
			}
		}
		scopes = append(scopes, coreScope)
	}
	return markContentReads(permissions, eligible), scopes, nil
}

// contentReadEligible reports whether an unscoped `read` of this resource kind
// may be admitted through the content-read branch
// (business.WorkContextPermission.ContentRead). It is a predicate rather than a
// fixed set because a mint and a recheck answer it from different places, and
// that difference is load-bearing: see mintContentReads and recheckContentReads.
type contentReadEligible func(resourceKind string) bool

// noContentReads admits nothing, so an unflagged permission keeps requiring an
// organization-level role exactly as it did before the branch existed.
func noContentReads(string) bool { return false }

// mintContentReads admits only the content the audience this capability is
// being minted for declares as its own.
//
// That audience is the one module that will ever present the capability back to
// this host, and both module-facing reads already refuse a scope outside its
// declaration (ModuleContentResources, read by ListReadableSourceCollections
// and CheckWorkContextRecordAccess). Admitting the composition-wide union here
// would seal scopes no consumer can use, and would let one module's declaration
// decide what a capability minted for another module may carry.
//
// An unregistered audience, or one that declares no content, admits nothing.
func mintContentReads(audience string) contentReadEligible {
	if service == nil || audience == "" {
		return noContentReads
	}
	declared, err := service.ModuleContentResources(audience)
	if err != nil {
		return noContentReads
	}
	return func(resourceKind string) bool { return slices.Contains(declared, resourceKind) }
}

// recheckContentReads admits every unscoped read, and consults no declaration
// at all. That is deliberate, and it is the narrower choice rather than the
// wider one.
//
// A recheck issues nothing. It re-validates a capability whose scopes a mint
// already sealed under mintContentReads, and the module-facing reads gate those
// scopes a second time against the audience's own declaration. So a recheck
// that is a superset of every mint can admit nothing that was not already
// issued — while the reverse, a recheck narrower than the mint that issued the
// capability, reports a live capability as revoked.
//
// That reverse is not hypothetical. The declaration is the module registry,
// which each replica derives on its own reconcile pass, so two hosts in one
// fleet disagree about it for up to one pass after any change: a capability minted by a host that
// declares the type is refused by a host that does not, and the consumer reads
// ErrWorkContextAuthorizationStale as a revocation. Resolving a recheck without
// the declaration takes the registry out of the staleness verdict. Revocation is still caught, by the live grant, share and platform
// bases the branch requires and by the authorization revision compared after.
func recheckContentReads() contentReadEligible {
	return func(string) bool { return true }
}

// markContentReads flags every unscoped `read` the given rule admits.
func markContentReads(
	permissions []business.WorkContextPermission,
	eligible contentReadEligible,
) []business.WorkContextPermission {
	for i := range permissions {
		permission := &permissions[i]
		permission.ContentRead = permission.ResourceID == "" &&
			permission.Action == "read" &&
			eligible(permission.ResourceKind)
	}
	return permissions
}

func permissionsFromCoreScopes(
	scopes []*basev0.WorkScopeV1,
	eligible contentReadEligible,
) []business.WorkContextPermission {
	permissions := make([]business.WorkContextPermission, 0)
	for _, scope := range scopes {
		for _, action := range scope.GetActions() {
			if len(scope.GetResourceIds()) == 0 {
				permissions = append(permissions, business.WorkContextPermission{
					ResourceKind: scope.GetResourceKind(),
					Action:       action,
				})
				continue
			}
			for _, resourceID := range scope.GetResourceIds() {
				permissions = append(permissions, business.WorkContextPermission{
					ResourceKind: scope.GetResourceKind(),
					Action:       action,
					ResourceID:   resourceID,
				})
			}
		}
	}
	return markContentReads(permissions, eligible)
}

func cloneWorkScopes(scopes []*basev0.WorkScopeV1) []*basev0.WorkScopeV1 {
	out := make([]*basev0.WorkScopeV1, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, &basev0.WorkScopeV1{
			ResourceKind: scope.GetResourceKind(),
			Actions:      append([]string(nil), scope.GetActions()...),
			ResourceIds:  append([]string(nil), scope.GetResourceIds()...),
		})
	}
	return out
}

func workContextReplayPolicy(value gen.WorkContextReplayPolicy) string {
	if value == gen.WorkContextReplayPolicy_WORK_CONTEXT_REPLAY_POLICY_SINGLE_USE {
		return workcontext.ReplaySingleUse
	}
	return workcontext.ReplayIdempotent
}

func workContextTTL(seconds int32) time.Duration {
	if seconds == 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func issuedWorkContext(
	token string,
	signed *basev0.WorkContextV1,
) *gen.IssuedWorkContext {
	currentActor := signed.GetOwnerPrincipalId()
	if actors := signed.GetActorChain(); len(actors) > 0 {
		currentActor = actors[len(actors)-1].GetPrincipalId()
	}
	response := &gen.IssuedWorkContext{
		Token:                   token,
		OrgId:                   signed.GetTenantId(),
		OwnerPrincipalId:        signed.GetOwnerPrincipalId(),
		TaskId:                  signed.GetTaskId(),
		SessionId:               signed.GetSessionId(),
		CurrentActorPrincipalId: currentActor,
		AuthorizationRevision:   signed.GetAuthorizationRevision(),
		ExpiresAt:               timestamppb.New(time.Unix(signed.GetExpiresAtUnix(), 0).UTC()),
	}
	if signed.ParentSessionId != nil {
		parent := signed.GetParentSessionId()
		response.ParentSessionId = &parent
	}
	if signed.WorkspaceId != nil {
		workspaceID := signed.GetWorkspaceId()
		response.WorkspaceId = &workspaceID
	}
	if signed.ProjectId != nil {
		projectID := signed.GetProjectId()
		response.ProjectId = &projectID
	}
	return response
}

// mapWorkContextError maps a refusal from core's minter. ErrRevoked — the
// parent or the authority moved underneath the mint — is a precondition the
// caller can re-establish; ErrInvalid is a request core will never sign.
func mapWorkContextError(err error) error {
	if _, isStatus := status.FromError(err); isStatus && status.Code(err) != codes.Unknown {
		return err
	}
	switch {
	case errors.Is(err, workcontext.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, workcontext.ErrRevoked), errors.Is(err, workcontext.ErrNoSeal):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrWorkContextAuthorityUnconfigured):
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	return status.Error(codes.Internal, "cannot issue Work Context")
}

var _ gen.WorkContextServiceServer = (*WorkContextAuthorityServer)(nil)
