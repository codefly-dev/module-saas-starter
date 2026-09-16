package business

import (
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CheckDelegatedRecordAccess intersects the existing placed-record oracle for
// every authenticated subject within one tenant read snapshot. No scope hierarchy or
// permission rule is duplicated here.
func (s *Service) CheckDelegatedRecordAccess(ctx context.Context, org string, subjects []string, resource, id, action string, current func(context.Context) error) (*gen.CheckWorkContextRecordAccessResponse, error) {
	if org == "" || len(subjects) == 0 || current == nil {
		return nil, status.Error(codes.PermissionDenied, "viewer authority required")
	}
	decision := &gen.CheckWorkContextRecordAccessResponse{}
	err := s.store.WithSourceReadSnapshot(ctx, org, func(ctx context.Context) error {
		return s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
			if err := current(ctx); err != nil {
				return err
			}
			for _, subject := range subjects {
				if subject == "" {
					return status.Error(codes.PermissionDenied, "viewer authority required")
				}
				ok, _, err := s.store.CheckAccess(ctx, subject, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, resource, id, action)
				if err != nil {
					return err
				}
				if !ok {
					return nil
				}
			}
			if err := current(ctx); err != nil {
				return err
			}
			node, err := s.store.RecordScopeNodeID(ctx, resource, id)
			if err != nil {
				return err
			}
			if node == "" {
				return nil
			}
			decision.Allowed = true
			decision.ScopeNodeId = node
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return decision, nil
}
