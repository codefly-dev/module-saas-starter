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

// The (resource_type, action) pair a solution-visibility question is asked in
// (issue #949). A subject may see an installed solution when it holds an
// accessible scope at that installation's authority-root node for this pair —
// resolved through the same grant + share union CheckAccess resolves, so a
// projection can never offer what an authority check would deny.
//
// Host-owned, exactly like the node kinds above: a solution install, its scope
// node and its registry record are all the host's, so naming the permission that
// governs *using* one names nothing above this module. A role that should reveal
// a solution permits (solution, use) or carries the wildcard; a role that permits
// only some product resource does not, which is what keeps a grant written for
// another purpose from quietly revealing a menu entry.
const (
	ResourceTypeSolution = "solution"
	ActionUseSolution    = "use"
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
		if err := s.store.RegisterScopeNode(ctx, node); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventScopeNodeRegistered, "scope_node", node.Id, req.OrgId,
			map[string]any{"scope_path": node.ScopePath, "kind": node.Kind})
	}); err != nil {
		return nil, w.Wrapf(err, "cannot register scope node")
	}
	return &gen.RegisterScopeNodeResponse{Node: node}, nil
}

// GrantScope grants a role to a principal/team at a registered scope node.
func (s *Service) GrantScope(ctx context.Context, actorID string, req *gen.GrantScopeRequest) (*gen.GrantScopeResponse, error) {
	w := wool.Get(ctx).In("GrantScope")
	var grant *gen.ScopeGrant
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		var e error
		// No cause: GrantScope is an administrator writing a grant. A derived
		// role is refused here, in the store, exactly because this path exists.
		grant, e = s.grantScopeTx(ctx, actorID, req, "")
		return e
	}); err != nil {
		return nil, w.Wrapf(err, "cannot grant scope")
	}
	return &gen.GrantScopeResponse{Grant: grant}, nil
}

// grantScopeTx is one scope grant inside the caller's organization transaction:
// the subject's tenancy (requireSubjectInOrgTx, under the membership lock a
// removal takes), the row, the tenant-visible scope.granted event and the audit
// record, all in that transaction. GrantScope runs it on its own;
// GrantInstallationToTeam runs it in the transaction that locked the
// installation, so the grant cannot land on an installation already uninstalled.
//
// causeGrantID names the grant this one is DERIVED FROM, and is empty for every
// grant an administrator writes. It is a parameter rather than a request field
// on purpose: GrantScopeRequest crosses the network, and a field a caller could
// set would let one claim a cause and so hand itself a derived role — the exact
// hand-grant the derivation exists to make impossible. The store refuses a
// derived role whenever this is empty (derived_module_access.go).
func (s *Service) grantScopeTx(ctx context.Context, actorID string, req *gen.GrantScopeRequest, causeGrantID string) (*gen.ScopeGrant, error) {
	if err := s.requireSubjectInOrgTx(ctx, req.OrgId, req.SubjectId, req.SubjectKind); err != nil {
		return nil, err
	}
	grant := &gen.ScopeGrant{
		Id:                 NewIDString(),
		OrgId:              req.OrgId,
		SubjectId:          req.SubjectId,
		SubjectKind:        req.SubjectKind,
		ScopePath:          req.ScopePath,
		RoleId:             req.RoleId,
		GrantedBy:          actorID,
		ExpiresAt:          req.ExpiresAt,
		DerivedFromGrantId: causeGrantID,
	}
	if e := s.store.GrantScope(ctx, grant); e != nil {
		return nil, e
	}
	// scope.granted is tenant-visible so a solution can track the boundaries
	// it holds; published in the grant's transaction (outbox). The boundary
	// is the scope node path the grant targets.
	if e := s.publishLifecycleEvent(ctx, EventScopeGranted, req.OrgId,
		req.ScopePath, actorID, map[string]any{
			"scope_grant_id": grant.Id,
			"role_id":        req.RoleId,
			"subject_id":     req.SubjectId,
			"scope_path":     req.ScopePath,
		}); e != nil {
		return nil, e
	}
	if e := s.emitTx(ctx, actorID, "user", EventScopeGranted, "scope_grant", grant.Id, req.OrgId,
		map[string]any{"role_id": req.RoleId, "subject_id": req.SubjectId, "scope_path": req.ScopePath}); e != nil {
		return nil, e
	}
	return grant, nil
}

// RevokeScope removes a hierarchical scope grant.
//
// A NARROWING, so it applies directly (narrow): the delete and its audit
// record commit in one control-plane transaction. The statement below names
// req.OrgId explicitly, so the delete stays confined to the organisation the
// tenant policy would have confined it to.
func (s *Service) RevokeScope(ctx context.Context, actorID string, req *gen.RevokeScopeRequest) error {
	w := wool.Get(ctx).In("RevokeScope")
	if err := s.narrow(ctx, func(ctx context.Context) error {
		return s.revokeScopeApply(ctx, actorID, req)
	}); err != nil {
		return w.Wrapf(err, "cannot revoke scope")
	}
	return nil
}

// revokeScopeApply is one scope revocation inside a narrowing transaction: the
// delete, the tenant-visible scope.revoked event and the audit record.
// RevokeScope and RevokeInstallationFromTeam both run it, so a team removed
// from an installation is recorded exactly as any other scope revocation is.
func (s *Service) revokeScopeApply(ctx context.Context, actorID string, req *gen.RevokeScopeRequest) error {
	if e := s.store.RevokeScope(ctx, req.OrgId, req.SubjectId, req.SubjectKind, req.ScopePath, req.RoleId); e != nil {
		return e
	}
	// scope.revoked is tenant-visible: a solution learns a boundary it held
	// went away (RFC-0004 open question 4). Published in the revoke's
	// transaction (outbox); boundary is the scope node path.
	if e := s.publishLifecycleEvent(ctx, EventScopeRevoked, req.OrgId,
		req.ScopePath, actorID, map[string]any{
			"role_id":    req.RoleId,
			"subject_id": req.SubjectId,
			"scope_path": req.ScopePath,
		}); e != nil {
		return e
	}
	return s.emitTx(ctx, actorID, "user", EventScopeRevoked, "scope_grant", req.RoleId, req.OrgId,
		map[string]any{"subject_id": req.SubjectId, "scope_path": req.ScopePath})
}

// ShareRecord grants a principal/team a role on a specific record.
//
// The share names a subject of the share's organization: record_shares' RLS
// policy checks only the share's organization and nothing ties subject_id to a
// membership, so the subject is checked in the share's own transaction, under
// the membership lock a removal takes (requireSubjectInOrgTx).
func (s *Service) ShareRecord(ctx context.Context, actorID string, req *gen.ShareRecordRequest) (*gen.ShareRecordResponse, error) {
	orgID, subjectID, err := canonicalSubjectTenancyIDs(req.OrgId, req.SubjectId)
	if err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("ShareRecord")
	share := &gen.RecordShare{
		Id:           NewIDString(),
		OrgId:        orgID,
		ResourceType: req.ResourceType,
		ResourceId:   req.ResourceId,
		SubjectId:    subjectID,
		SubjectKind:  req.SubjectKind,
		RoleId:       req.RoleId,
		GrantedBy:    actorID,
		ExpiresAt:    req.ExpiresAt,
	}
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		if err := s.requireSubjectInOrgTx(ctx, orgID, subjectID, req.SubjectKind); err != nil {
			return err
		}
		if err := s.store.ShareRecord(ctx, share); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventRecordShared, req.ResourceType, req.ResourceId, orgID,
			map[string]any{"role_id": req.RoleId, "subject_id": subjectID})
	}); err != nil {
		return nil, w.Wrapf(err, "cannot share record")
	}
	return &gen.ShareRecordResponse{Share: share}, nil
}

// RevokeShare removes a per-record share.
//
// A NARROWING, so it applies directly, exactly as RevokeScope above does. The
// delete names req.OrgId in its WHERE, so it stays confined to the organisation
// the tenant policy would have confined it to.
func (s *Service) RevokeShare(ctx context.Context, actorID string, req *gen.RevokeShareRequest) error {
	w := wool.Get(ctx).In("RevokeShare")
	if err := s.narrow(ctx, func(ctx context.Context) error {
		if err := s.store.RevokeShare(ctx, req.OrgId, req.ResourceType, req.ResourceId, req.SubjectId, req.SubjectKind, req.RoleId); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventRecordShareRevoked, req.ResourceType, req.ResourceId, req.OrgId,
			map[string]any{"role_id": req.RoleId, "subject_id": req.SubjectId})
	}); err != nil {
		return w.Wrapf(err, "cannot revoke record share")
	}
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

func (s *Service) ListCollectionAccess(ctx context.Context, req *gen.ListCollectionAccessRequest) (*gen.ListCollectionAccessResponse, error) {
	size := int(req.PageSize)
	if size <= 0 || size > 100 {
		size = 100
	}
	var collections []*gen.CollectionAccess
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		var err error
		collections, err = s.store.ListCollectionAccess(ctx, req.OrgId, req.PageToken, size+1, s.declaredModules().ContentResources())
		return err
	}); err != nil {
		return nil, err
	}
	response := &gen.ListCollectionAccessResponse{Collections: collections}
	if len(collections) > size {
		response.Collections = collections[:size]
		response.NextPageToken = collections[size-1].Node.ScopePath
	}
	return response, nil
}
