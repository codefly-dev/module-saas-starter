package business

import (
	"context"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
)

// Scope-node kind conventions (issue #473; documented in AUTHZ.md). A `solution`
// node is the one node per solution install under the org root; a `collection`
// node is a data container a solution creates under its node — the data boundary
// a datasource binds to. Every other kind stays product-defined; the registry
// stores kind verbatim and never branches on it, so these are conventions, not
// enforced enums.
const (
	ScopeNodeKindSolution   = "solution"
	ScopeNodeKindCollection = "collection"
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

// listAccessibleScopesDefaultPageSize / …MaxPageSize bound the result: a subject
// entitled at a broad ancestor can reach every node in that subtree (including
// every placed record), so an unbounded list would be a scaling hazard. The max
// mirrors the proto ceiling.
const (
	listAccessibleScopesDefaultPageSize = 500
	listAccessibleScopesMaxPageSize     = 1000
)

// ListAccessibleScopes enumerates the scope nodes a subject may act on with
// (resource_type, action) — the list-objects companion to CheckAccess. Always
// org-scoped, so it runs under WithOrgTx; the store resolves the same grant +
// share union as CheckAccess, so the two never disagree. Cursor-paginated on
// scope_path: an over-fetch of one row detects whether a further page exists.
func (s *Service) ListAccessibleScopes(ctx context.Context, req *gen.ListAccessibleScopesRequest) (*gen.ListAccessibleScopesResponse, error) {
	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = listAccessibleScopesDefaultPageSize
	}
	if pageSize > listAccessibleScopesMaxPageSize {
		pageSize = listAccessibleScopesMaxPageSize
	}

	var scopes []*gen.AccessibleScope
	wrap := func(ctx context.Context) error {
		out, err := s.store.ListAccessibleScopes(ctx, req.OrgId, req.SubjectId, req.SubjectKind, req.ResourceType, req.Action, req.PageToken, pageSize+1)
		scopes = out
		return err
	}
	if err := s.store.WithOrgTx(ctx, req.OrgId, wrap); err != nil {
		return nil, err
	}

	var nextToken string
	if len(scopes) > pageSize {
		scopes = scopes[:pageSize]
		nextToken = scopes[pageSize-1].ScopePath
	}
	return &gen.ListAccessibleScopesResponse{Scopes: scopes, NextPageToken: nextToken}, nil
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
		if e := s.store.GrantScope(ctx, grant); e != nil {
			return e
		}
		// scope.granted is tenant-visible so a solution can track the boundaries
		// it holds; published in the grant's transaction (outbox). The boundary
		// is the scope node path the grant targets.
		return s.publishLifecycleEvent(ctx, EventScopeGranted, req.OrgId,
			req.ScopePath, actorID, map[string]any{
				"scope_grant_id": grant.Id,
				"role_id":        req.RoleId,
				"subject_id":     req.SubjectId,
				"scope_path":     req.ScopePath,
			})
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
		if e := s.store.RevokeScope(ctx, req.OrgId, req.SubjectId, req.SubjectKind, req.ScopePath, req.RoleId); e != nil {
			return e
		}
		// scope.revoked is tenant-visible: a solution learns a boundary it held
		// went away (RFC-0004 open question 4). Published in the revoke's
		// transaction (outbox); boundary is the scope node path.
		return s.publishLifecycleEvent(ctx, EventScopeRevoked, req.OrgId,
			req.ScopePath, actorID, map[string]any{
				"role_id":    req.RoleId,
				"subject_id": req.SubjectId,
				"scope_path": req.ScopePath,
			})
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
