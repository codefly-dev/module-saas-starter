package adapters

import (
	"context"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ModuleCapabilitiesServer serves the module-facing capability surface (issue
// #463) on the accounts internal listener. It authenticates the caller from the
// forwarded Work Context and delegates every capability to the shared
// business.Service, which enforces the per-principal grant.
type ModuleCapabilitiesServer struct {
	gen.UnsafeModuleCapabilitiesServiceServer
}

// moduleCapabilitiesSingleton is shared between the raw-gRPC internal listener
// and the Connect listener, mirroring the other cross-protocol singletons.
var moduleCapabilitiesSingleton = &ModuleCapabilitiesServer{}

// ModuleCapabilitiesSingleton returns the shared server instance.
func ModuleCapabilitiesSingleton() *ModuleCapabilitiesServer { return moduleCapabilitiesSingleton }

// moduleCaller resolves the authenticated module service principal.
func moduleCaller(ctx context.Context) (business.ModuleCaller, error) {
	id, err := callerID(ctx)
	if err != nil {
		return business.ModuleCaller{}, err
	}
	return business.ModuleCaller{PrincipalID: id, BoundOrg: callerOrg(ctx)}, nil
}

func (s *ModuleCapabilitiesServer) EnqueueJob(ctx context.Context, req *gen.ModuleEnqueueJobRequest) (*gen.ModuleEnqueueJobResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := service.ModuleEnqueueJob(ctx, caller, &jobsv1.EnqueueJobRequest{Job: req.GetJob()})
	if err != nil {
		return nil, err
	}
	return &gen.ModuleEnqueueJobResponse{JobId: resp.GetJobId(), Disposition: resp.GetDisposition()}, nil
}

func (s *ModuleCapabilitiesServer) ClaimJobs(ctx context.Context, req *gen.ModuleClaimJobsRequest) (*gen.ModuleClaimJobsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := service.ModuleClaimJobs(ctx, caller, &jobsv1.ClaimJobsRequest{
		Queue:         req.GetQueue(),
		WorkerId:      req.GetWorkerId(),
		Limit:         req.GetLimit(),
		LeaseDuration: req.GetLeaseDuration(),
	})
	if err != nil {
		return nil, err
	}
	return &gen.ModuleClaimJobsResponse{Jobs: resp.GetJobs()}, nil
}

func (s *ModuleCapabilitiesServer) HeartbeatJob(ctx context.Context, req *gen.ModuleHeartbeatJobRequest) (*gen.ModuleHeartbeatJobResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := service.ModuleHeartbeatJob(ctx, caller, &jobsv1.HeartbeatJobRequest{Lease: req.GetLease(), Extension: req.GetExtension()})
	if err != nil {
		return nil, err
	}
	return &gen.ModuleHeartbeatJobResponse{Lease: resp.GetLease()}, nil
}

func (s *ModuleCapabilitiesServer) AckJob(ctx context.Context, req *gen.ModuleAckJobRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := service.ModuleAckJob(ctx, caller, &jobsv1.CompleteJobRequest{Lease: req.GetLease()}); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *ModuleCapabilitiesServer) NackJob(ctx context.Context, req *gen.ModuleNackJobRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := service.ModuleNackJob(ctx, caller, req.GetLease(), req.GetFailure(), req.GetRetryable(), req.GetRetryAt()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *ModuleCapabilitiesServer) NotifyUser(ctx context.Context, req *gen.ModuleNotifyUserRequest) (*gen.ModuleNotifyUserResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	result, err := service.ModuleNotifyUser(ctx, caller,
		req.GetTenant(), req.GetUserId(), req.GetTitle(), req.GetBody(),
		req.GetType(), req.GetActionUrl(), req.GetCategory(), req.GetIdempotencyKey())
	if err != nil {
		return nil, err
	}
	return &gen.ModuleNotifyUserResponse{NotificationId: result.NotificationID, Delivered: result.Delivered}, nil
}

func (s *ModuleCapabilitiesServer) RequestApproval(ctx context.Context, req *gen.ModuleRequestApprovalRequest) (*gen.ModuleRequestApprovalResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	resume := req.GetResumeRef()
	id, err := service.ModuleRequestApproval(ctx, caller, business.ModuleRequestApprovalInput{
		Tenant:        req.GetTenant(),
		Resource:      req.GetResource(),
		Action:        req.GetAction(),
		Subject:       structToMap(req.GetSubject()),
		RequestedBy:   req.GetRequestedBy(),
		Quorum:        int(req.GetPolicy().GetQuorum()),
		ApproverSet:   req.GetPolicy().GetApproverSet(),
		AllowSelf:     req.GetPolicy().GetAllowSelf(),
		ResumeQueue:   resume.GetQueue(),
		ResumeTopic:   resume.GetTopic(),
		ResumePayload: structToMap(resume.GetPayload()),
		ExpiresAt:     timePtr(req.GetExpiresAt()),
		EscalateAt:    timePtr(req.GetEscalateAt()),
	})
	if err != nil {
		return nil, err
	}
	return &gen.ModuleRequestApprovalResponse{ApprovalId: id}, nil
}

func (s *ModuleCapabilitiesServer) GetApproval(ctx context.Context, req *gen.ModuleGetApprovalRequest) (*gen.ModuleApproval, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	approval, err := service.ModuleGetApproval(ctx, caller, req.GetTenant(), req.GetApprovalId())
	if err != nil {
		return nil, err
	}
	return moduleApprovalProto(approval), nil
}

func (s *ModuleCapabilitiesServer) CancelApproval(ctx context.Context, req *gen.ModuleCancelApprovalRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := service.ModuleCancelApproval(ctx, caller, req.GetTenant(), req.GetApprovalId(), req.GetReason()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *ModuleCapabilitiesServer) EmitAuditEvent(ctx context.Context, req *gen.ModuleEmitAuditEventRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := service.ModuleEmitAuditEvent(ctx, caller,
		req.GetTenant(), req.GetEventType(), req.GetActor(), req.GetSolution(), req.GetEntryId(), req.GetFields()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func structToMap(s *structpb.Struct) map[string]any {
	if s == nil {
		return nil
	}
	return s.AsMap()
}

func mapToStruct(m map[string]any) *structpb.Struct {
	if m == nil {
		return nil
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return s
}

func timePtr(t *timestamppb.Timestamp) *time.Time {
	if t == nil {
		return nil
	}
	v := t.AsTime()
	return &v
}

func timeProto(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

func moduleApprovalProto(a *business.ApprovalRequest) *gen.ModuleApproval {
	if a == nil {
		return nil
	}
	return &gen.ModuleApproval{
		Id:          a.ID,
		Tenant:      a.OrgID,
		Resource:    a.Resource,
		Action:      a.Action,
		Subject:     mapToStruct(a.Subject),
		RequestedBy: a.RequestedBy,
		Quorum:      uint32(a.Quorum),
		State:       string(a.State),
		ResumeRef: &gen.ModuleResumeRef{
			Queue:   a.ResumeRef.Queue,
			Topic:   a.ResumeRef.Topic,
			Payload: mapToStruct(a.ResumeRef.Payload),
		},
		ExpiresAt:  timeProto(a.ExpiresAt),
		EscalateAt: timeProto(a.EscalateAt),
		CreatedAt:  timestamppb.New(a.CreatedAt),
	}
}
