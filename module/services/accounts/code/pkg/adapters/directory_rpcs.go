package adapters

import (
	"context"

	"connectrpc.com/connect"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// DirectoryServer handles DirectoryService RPCs — the read-only organization
// directory. It is a separate service from OrganizationService and TeamService
// so a published client can bind these reads without the administrative
// surfaces a per-file generated binding would otherwise drag in.
//
// Each RPC IS the corresponding OrganizationService/TeamService read: both call
// the one function below, so the validation, authentication, membership check
// and query are the ones those services already enforce, never a second copy
// that could drift from them.
type DirectoryServer struct {
	gen.UnsafeDirectoryServiceServer
}

// ListOrganizationMembers is OrganizationService.ListMembers.
func (s *DirectoryServer) ListOrganizationMembers(ctx context.Context, req *gen.ListOrgMembersRequest) (*gen.ListOrgMembersResponse, error) {
	return listOrganizationMembers(ctx, req)
}

// ListTeams is TeamService.ListTeams.
func (s *DirectoryServer) ListTeams(ctx context.Context, req *gen.ListTeamsRequest) (*gen.ListTeamsResponse, error) {
	return listOrganizationTeams(ctx, req)
}

// ListTeamMembers is TeamService.ListMembers.
func (s *DirectoryServer) ListTeamMembers(ctx context.Context, req *gen.ListTeamMembersRequest) (*gen.ListTeamMembersResponse, error) {
	return listTeamMembers(ctx, req)
}

type directoryConnectHandler struct{ inner *DirectoryServer }

func (h *directoryConnectHandler) ListOrganizationMembers(ctx context.Context, req *connect.Request[gen.ListOrgMembersRequest]) (*connect.Response[gen.ListOrgMembersResponse], error) {
	return unary(ctx, req, h.inner.ListOrganizationMembers)
}

func (h *directoryConnectHandler) ListTeams(ctx context.Context, req *connect.Request[gen.ListTeamsRequest]) (*connect.Response[gen.ListTeamsResponse], error) {
	return unary(ctx, req, h.inner.ListTeams)
}

func (h *directoryConnectHandler) ListTeamMembers(ctx context.Context, req *connect.Request[gen.ListTeamMembersRequest]) (*connect.Response[gen.ListTeamMembersResponse], error) {
	return unary(ctx, req, h.inner.ListTeamMembers)
}
