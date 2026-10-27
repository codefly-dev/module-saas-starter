package adapters

// The artifact interface (codefly.dev/artifact): a person's consent to an
// exact authored artifact a module runs, for one of its installations, under
// one policy.
//
// The decision is the shared authority core's, once for every host
// (authority.Service.DecideArtifact over decide.Artifact), so this host and
// the local authority allow and refuse the same calls for the same reasons: a
// policy is a resource kind the module serves; approving and revoking need the
// person present holding that kind's `activate` action and a run its `run`
// action, on the whole kind — holding the action is the whole requirement, and
// no administrator tier is asked for; the installation is the module's own,
// active, at the parent's organization; and an artifact's source is that
// installation's namespace, exactly as InstallationService.Current names it.
//
// What this file adds is the wire and the records: the module's workload
// credential and the parent it presents go to the core, and the approval the
// core permits is written to and read from accounts' own store
// (business.ApproveHostArtifact and its siblings, which keep no rule of their
// own). The SaaS-only ModuleCapabilitiesService artifact path is a different
// face, with its own installed-policy semantics; it is not this decision.

import (
	"context"

	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	artifactv1 "accounts/pkg/gen/host/artifact/v1"
)

// HostArtifactServer serves host.artifact.v1.
type HostArtifactServer struct {
	artifactv1.UnimplementedArtifactServiceServer
}

// Approve records the person's consent to the artifact: the policy's activate
// action, held by the person present.
func (s *HostArtifactServer) Approve(ctx context.Context, req *artifactv1.ArtifactRequest) (*artifactv1.Approval, error) {
	decided, err := artifactDecide(ctx, req.GetParentToken(), req.GetActorCredential(), req.GetInstallationId(), req.GetPolicy(), decide.ArtifactActivate, artifactIdentity(req))
	if err != nil {
		return nil, err
	}
	out, err := service.ApproveHostArtifact(decided.ctx, decided.tenant, decided.owner, decided.installation, decided.module,
		req.GetPolicy(), decided.digest, decided.subject, req.GetIdentity().GetExpectedRevision())
	return artifactApprovalToWire(out, err)
}

// Authorize answers whether a standing approval covers the artifact, for a
// run: the policy's run action, which a context a module acts through carries.
func (s *HostArtifactServer) Authorize(ctx context.Context, req *artifactv1.ArtifactRequest) (*artifactv1.Approval, error) {
	decided, err := artifactDecide(ctx, req.GetParentToken(), req.GetActorCredential(), req.GetInstallationId(), req.GetPolicy(), decide.ArtifactRun, artifactIdentity(req))
	if err != nil {
		return nil, err
	}
	out, err := service.AuthorizedHostArtifact(decided.ctx, decided.tenant, decided.owner, decided.installation, decided.module,
		req.GetPolicy(), decided.digest, decided.subject, req.GetIdentity().GetExpectedRevision())
	return artifactApprovalToWire(out, err)
}

// Revoke withdraws an approval, for the person present: the policy's activate
// action, as consent itself is. It names no artifact, so the revision it
// answers is read back from the record it ends.
func (s *HostArtifactServer) Revoke(ctx context.Context, req *artifactv1.RevokeRequest) (*artifactv1.Approval, error) {
	decided, err := artifactDecide(ctx, req.GetParentToken(), req.GetActorCredential(), req.GetInstallationId(), req.GetPolicy(), decide.ArtifactActivate, nil)
	if err != nil {
		return nil, err
	}
	out, err := service.RevokeHostArtifact(decided.ctx, decided.tenant, decided.owner, decided.installation, decided.module, req.GetPolicy(), req.GetApprovalId(), approvedArtifactRevision)
	return artifactApprovalToWire(out, err)
}

// artifactDecided is one decision the core made, and what the records need of
// it: whose organization, who consented, which installation and module
// principal, and the artifact's digest and canonical bytes.
type artifactDecided struct {
	ctx                         context.Context
	tenant, owner, installation string
	module, digest              string
	subject                     []byte
}

// artifactDecide hands the request to the core and keeps what the records
// need. Every refusal is the core's, named: a policy the module does not
// serve, a parent that does not hold the action or holds it on some ids only,
// an installation that is not the module's own active one at the parent's
// organization (a foreign one and a missing one answer alike), a source that
// is not the installation's namespace, a module acting between the person and
// consent. A request missing what it must name is invalid, not refused.
func artifactDecide(ctx context.Context, parent, credential, installation, policy, action string, identity *artifactv1.Identity) (artifactDecided, error) {
	core, err := authorityCore()
	if err != nil {
		return artifactDecided{}, err
	}
	actor, err := workload(ctx, credential, "the actor")
	if err != nil {
		return artifactDecided{}, status.Error(codes.PermissionDenied, status.Convert(err).Message())
	}
	var subject []byte
	var asked *decide.ArtifactIdentity
	if identity != nil {
		subject, err = (proto.MarshalOptions{Deterministic: true}).Marshal(identity)
		if err != nil {
			return artifactDecided{}, status.Errorf(codes.InvalidArgument, "the artifact identity does not encode: %v", err)
		}
		asked = &decide.ArtifactIdentity{Schema: identity.GetSchema(), Source: identity.GetSource(), Subject: identity.GetSubject(),
			ExpectedRevision: identity.GetExpectedRevision()}
		for _, c := range identity.GetContracts() {
			asked.Contracts = append(asked.Contracts, decide.ArtifactContract{Kind: c.GetKind(), Name: c.GetName(), Digest: c.GetDigest()})
		}
	}
	inst, err := core.DecideArtifact(ctx, parent, actor, installation, policy, action, asked)
	if err != nil {
		return artifactDecided{}, coreStatus(err)
	}
	// The decision verified the parent, so its owner and tenant are the
	// installation's: the records are written as that person, in that tenant.
	verified, _, err := core.Check(ctx, parent, actor.Module)
	if err != nil {
		return artifactDecided{}, coreStatus(err)
	}
	claims := verified.Context()
	out := artifactDecided{
		ctx:    auth.WithVerifiedDatabaseIdentity(ctx, claims.GetOwnerPrincipalId(), claims.GetTenantId()),
		tenant: claims.GetTenantId(), owner: claims.GetOwnerPrincipalId(), installation: inst.ID,
		module: business.ModulePrincipalID(actor.Module), subject: subject,
	}
	if asked != nil {
		out.digest = decide.ArtifactDigest(*asked)
	}
	return out, nil
}

// approvedArtifactRevision is the revision a retained approval was given at,
// read from the identity it kept. An approval whose record does not decode
// answers revision 0 rather than failing the revoke: the consent is ended
// either way, and the revision is what the artifact said, not authority.
func approvedArtifactRevision(subject []byte) int64 {
	var identity artifactv1.Identity
	if proto.Unmarshal(subject, &identity) != nil {
		return 0
	}
	return identity.GetExpectedRevision()
}

// artifactApprovalToWire is an approval as the interface states it: the host's
// own qualified name for it, the artifact's digest, and the revision the
// artifact was approved at.
func artifactApprovalToWire(out *business.HostArtifactApproval, err error) (*artifactv1.Approval, error) {
	if err != nil {
		return nil, err
	}
	return &artifactv1.Approval{QualifiedName: "host/approved-artifacts/" + out.ID, Digest: out.Digest, ExpectedRevision: out.Revision}, nil
}

// An omitted identity is an empty identity for the core to reject, never the
// nil identity that selects its revoke decision.
func artifactIdentity(req *artifactv1.ArtifactRequest) *artifactv1.Identity {
	if identity := req.GetIdentity(); identity != nil {
		return identity
	}
	return &artifactv1.Identity{}
}
