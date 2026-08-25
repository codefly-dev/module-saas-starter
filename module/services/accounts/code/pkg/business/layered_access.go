package business

import (
	"context"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
)

// CheckAccess is the hierarchical + per-record authorization decision (#178),
// the companion to CheckPermission. Always org-scoped — a record lives in
// exactly one tenant — so it always runs under WithOrgTx. The store resolves the
// record's true scope from resource_id itself (never a caller-supplied path).
func (s *Service) CheckAccess(ctx context.Context, req *gen.CheckAccessRequest) (*gen.CheckAccessResponse, error) {
	var allowed bool
	var reason string
	wrap := func(ctx context.Context) error {
		a, r, err := s.store.CheckAccess(ctx, req.SubjectId, req.SubjectKind, req.ResourceType, req.ResourceId, req.Action)
		allowed, reason = a, r
		return err
	}
	if err := s.store.WithOrgTx(ctx, req.OrgId, wrap); err != nil {
		return nil, err
	}
	return &gen.CheckAccessResponse{Allowed: allowed, Reason: reason}, nil
}

// RegisterScopeNode adds a node to the org's scope tree (or places a product
// record at a node when resource fields are set).
func (s *Service) RegisterScopeNode(ctx context.Context, actorID string, req *gen.RegisterScopeNodeRequest) (*gen.RegisterScopeNodeResponse, error) {
	w := wool.Get(ctx).In("RegisterScopeNode")
	node := &gen.ScopeNode{
		Id:           NewIDString(),
		OrgId:        req.OrgId,
		ScopePath:    req.ScopePath,
		Kind:         req.Kind,
		Label:        req.Label,
		ResourceType: req.ResourceType,
		ResourceId:   req.ResourceId,
	}
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		return s.store.RegisterScopeNode(ctx, node)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot register scope node")
	}
	s.emit(ctx, actorID, "user", EventScopeNodeRegistered, "scope_node", node.Id, req.OrgId,
		map[string]any{"scope_path": node.ScopePath, "kind": node.Kind})
	return &gen.RegisterScopeNodeResponse{Node: node}, nil
}

// GrantScope grants a role to a principal/team at a registered scope node.
func (s *Service) GrantScope(ctx context.Context, actorID string, req *gen.GrantScopeRequest) (*gen.GrantScopeResponse, error) {
	w := wool.Get(ctx).In("GrantScope")
	grant := &gen.ScopeGrant{
		Id:          NewIDString(),
		OrgId:       req.OrgId,
		SubjectId:   req.SubjectId,
		SubjectKind: req.SubjectKind,
		ScopePath:   req.ScopePath,
		RoleId:      req.RoleId,
		GrantedBy:   actorID,
		ExpiresAt:   req.ExpiresAt,
	}
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		return s.store.GrantScope(ctx, grant)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot grant scope")
	}
	s.emit(ctx, actorID, "user", EventScopeGranted, "scope_grant", grant.Id, req.OrgId,
		map[string]any{"role_id": req.RoleId, "subject_id": req.SubjectId, "scope_path": req.ScopePath})
	return &gen.GrantScopeResponse{Grant: grant}, nil
}

// RevokeScope removes a hierarchical scope grant.
func (s *Service) RevokeScope(ctx context.Context, actorID string, req *gen.RevokeScopeRequest) error {
	w := wool.Get(ctx).In("RevokeScope")
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		return s.store.RevokeScope(ctx, req.OrgId, req.SubjectId, req.SubjectKind, req.ScopePath, req.RoleId)
	}); err != nil {
		return w.Wrapf(err, "cannot revoke scope")
	}
	s.emit(ctx, actorID, "user", EventScopeRevoked, "scope_grant", req.RoleId, req.OrgId,
		map[string]any{"subject_id": req.SubjectId, "scope_path": req.ScopePath})
	return nil
}

// ShareRecord grants a principal/team a role on a specific record.
func (s *Service) ShareRecord(ctx context.Context, actorID string, req *gen.ShareRecordRequest) (*gen.ShareRecordResponse, error) {
	w := wool.Get(ctx).In("ShareRecord")
	share := &gen.RecordShare{
		Id:           NewIDString(),
		OrgId:        req.OrgId,
		ResourceType: req.ResourceType,
		ResourceId:   req.ResourceId,
		SubjectId:    req.SubjectId,
		SubjectKind:  req.SubjectKind,
		RoleId:       req.RoleId,
		GrantedBy:    actorID,
		ExpiresAt:    req.ExpiresAt,
	}
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		return s.store.ShareRecord(ctx, share)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot share record")
	}
	s.emit(ctx, actorID, "user", EventRecordShared, req.ResourceType, req.ResourceId, req.OrgId,
		map[string]any{"role_id": req.RoleId, "subject_id": req.SubjectId})
	return &gen.ShareRecordResponse{Share: share}, nil
}

// RevokeShare removes a per-record share.
func (s *Service) RevokeShare(ctx context.Context, actorID string, req *gen.RevokeShareRequest) error {
	w := wool.Get(ctx).In("RevokeShare")
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		return s.store.RevokeShare(ctx, req.OrgId, req.ResourceType, req.ResourceId, req.SubjectId, req.SubjectKind, req.RoleId)
	}); err != nil {
		return w.Wrapf(err, "cannot revoke record share")
	}
	s.emit(ctx, actorID, "user", EventRecordShareRevoked, req.ResourceType, req.ResourceId, req.OrgId,
		map[string]any{"role_id": req.RoleId, "subject_id": req.SubjectId})
	return nil
}

// ListShares returns the shares on a specific record.
func (s *Service) ListShares(ctx context.Context, req *gen.ListSharesRequest) (*gen.ListSharesResponse, error) {
	w := wool.Get(ctx).In("ListShares")
	var shares []*gen.RecordShare
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		out, err := s.store.ListShares(ctx, req.OrgId, req.ResourceType, req.ResourceId)
		shares = out
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list record shares")
	}
	return &gen.ListSharesResponse{Shares: shares}, nil
}
