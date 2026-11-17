package adapters

import (
	"context"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RecordHopRefusal records one call the hop's front door refused itself in
// the audit journal. Its caller is the auth-gateway, which is the hop in a
// cell and keeps no journal: the hop's own refusals reach the authority from
// nobody else, so they reach the journal through here. The caller is trusted
// to say what it refused, so the internal credential is the whole admission,
// checked before the request is read.
func (s *WorkContextAuthorityServer) RecordHopRefusal(
	ctx context.Context, req *gen.RecordHopRefusalRequest,
) (*gen.RecordHopRefusalResponse, error) {
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	if err := Validate(req); err != nil {
		return nil, err
	}
	if service == nil {
		return nil, status.Error(codes.Unavailable, "this host keeps no audit journal")
	}
	service.RecordHopRefusal(ctx, business.HopRefusal{
		Method: req.GetMethod(), Code: req.GetCode(), Reason: req.GetReason(), Module: req.GetModule(),
		Organization: req.GetOrganizationId(), Owner: req.GetOwnerPrincipalId(), Actor: req.GetActorPrincipalId(),
		Binding: req.GetBindingId(), Task: req.GetTaskId(), Claimed: req.GetClaimed(),
	})
	return &gen.RecordHopRefusalResponse{}, nil
}
