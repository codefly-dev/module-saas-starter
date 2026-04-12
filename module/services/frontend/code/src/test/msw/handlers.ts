/**
 * MSW handlers for Connect RPC.
 *
 * Connect-web sends POST requests to /{package}.{Service}/{Method}
 * with JSON body. We intercept these with MSW's http.post handler.
 */

import { http, HttpResponse } from "msw";
import {
  mockUser,
  mockOrganization,
  mockAuditEvent,
  mockInvitation,
  mockSession,
  mockPlatformAdmin,
  mockFeatureFlag,
} from "./fixtures";

const BASE = "http://localhost:9999";

// Helper: Connect RPC endpoint path
function rpc(service: string, method: string) {
  return `${BASE}/customers.${service}/${method}`;
}

export const handlers = [
  // ── UserService ───────────────────────────────────────────
  http.post(rpc("UserService", "Version"), () =>
    HttpResponse.json({ version: "0.0.1" }),
  ),
  http.post(rpc("UserService", "GetSelf"), () =>
    HttpResponse.json({
      user: mockUser({ uuid: "self-user-id", primaryEmail: "me@test.com" }),
      identities: [],
      organizations: [mockOrganization()],
    }),
  ),
  http.post(rpc("UserService", "RegisterUser"), () =>
    HttpResponse.json({ user: mockUser(), identity: null }),
  ),
  http.post(rpc("UserService", "GetUser"), () =>
    HttpResponse.json(mockUser()),
  ),
  http.post(rpc("UserService", "ListUsers"), () =>
    HttpResponse.json({ users: [mockUser(), mockUser()] }),
  ),
  http.post(rpc("UserService", "UpdateUser"), () =>
    HttpResponse.json(mockUser()),
  ),
  http.post(rpc("UserService", "DeleteUser"), () =>
    HttpResponse.json({}),
  ),

  // ── AuthService ───────────────────────────────────────────
  http.post(rpc("AuthService", "Authenticate"), () =>
    HttpResponse.json({
      accessToken: "test-access-token",
      refreshToken: "test-refresh-token",
      expiresIn: "900",
      user: mockUser({ uuid: "auth-user-id" }),
    }),
  ),
  http.post(rpc("AuthService", "RefreshToken"), () =>
    HttpResponse.json({
      accessToken: "new-access-token",
      refreshToken: "new-refresh-token",
      expiresIn: "900",
    }),
  ),
  http.post(rpc("AuthService", "Logout"), () =>
    HttpResponse.json({}),
  ),

  // ── OrganizationService ───────────────────────────────────
  http.post(rpc("OrganizationService", "ListOrganizations"), () =>
    HttpResponse.json({ organizations: [mockOrganization(), mockOrganization()] }),
  ),
  http.post(rpc("OrganizationService", "GetOrganization"), () =>
    HttpResponse.json(mockOrganization()),
  ),
  http.post(rpc("OrganizationService", "CreateOrganization"), () =>
    HttpResponse.json({ organization: mockOrganization() }),
  ),
  http.post(rpc("OrganizationService", "ListMembers"), () =>
    HttpResponse.json({ members: [] }),
  ),
  http.post(rpc("OrganizationService", "AddMember"), () =>
    HttpResponse.json({}),
  ),
  http.post(rpc("OrganizationService", "RemoveMember"), () =>
    HttpResponse.json({}),
  ),

  // ── TeamService ───────────────────────────────────────────
  http.post(rpc("TeamService", "ListTeams"), () =>
    HttpResponse.json({ teams: [] }),
  ),
  http.post(rpc("TeamService", "CreateTeam"), () =>
    HttpResponse.json({ team: { id: "team-1", orgId: "org-1", name: "Engineering" } }),
  ),
  http.post(rpc("TeamService", "ListMembers"), () =>
    HttpResponse.json({ members: [] }),
  ),
  http.post(rpc("TeamService", "AddMember"), () =>
    HttpResponse.json({}),
  ),
  http.post(rpc("TeamService", "RemoveMember"), () =>
    HttpResponse.json({}),
  ),

  // ── PermissionService ─────────────────────────────────────
  http.post(rpc("PermissionService", "ListRoles"), () =>
    HttpResponse.json({ roles: [] }),
  ),
  http.post(rpc("PermissionService", "CreateRole"), () =>
    HttpResponse.json({ role: { id: "role-1", name: "Editor" } }),
  ),
  http.post(rpc("PermissionService", "DeleteRole"), () =>
    HttpResponse.json({}),
  ),
  http.post(rpc("PermissionService", "AssignRole"), () =>
    HttpResponse.json({ id: "assignment-1" }),
  ),
  http.post(rpc("PermissionService", "RevokeRole"), () =>
    HttpResponse.json({}),
  ),
  http.post(rpc("PermissionService", "CheckPermission"), () =>
    HttpResponse.json({ allowed: true }),
  ),

  // ── APIKeyService ─────────────────────────────────────────
  http.post(rpc("APIKeyService", "ListAPIKeys"), () =>
    HttpResponse.json({ keys: [] }),
  ),
  http.post(rpc("APIKeyService", "CreateAPIKey"), () =>
    HttpResponse.json({ key: { id: "key-1", name: "Test Key" }, plaintextKey: "sk_test_..." }),
  ),
  http.post(rpc("APIKeyService", "RevokeAPIKey"), () =>
    HttpResponse.json({}),
  ),

  // ── AuditService ──────────────────────────────────────────
  http.post(rpc("AuditService", "QueryAuditLog"), () =>
    HttpResponse.json({
      events: [mockAuditEvent(), mockAuditEvent()],
      totalCount: 2,
    }),
  ),

  // ── InvitationService ─────────────────────────────────────
  http.post(rpc("InvitationService", "ListInvitations"), () =>
    HttpResponse.json({ invitations: [mockInvitation()] }),
  ),
  http.post(rpc("InvitationService", "CreateInvitation"), () =>
    HttpResponse.json({ invitation: mockInvitation(), inviteToken: "inv_test_..." }),
  ),
  http.post(rpc("InvitationService", "AcceptInvitation"), () =>
    HttpResponse.json({ organization: mockOrganization() }),
  ),
  http.post(rpc("InvitationService", "RevokeInvitation"), () =>
    HttpResponse.json({}),
  ),

  // ── PlatformAdminService ──────────────────────────────────
  http.post(rpc("PlatformAdminService", "SearchUsers"), () =>
    HttpResponse.json({ users: [mockUser(), mockUser()], nextPageToken: "" }),
  ),
  http.post(rpc("PlatformAdminService", "SuspendUser"), () =>
    HttpResponse.json({}),
  ),
  http.post(rpc("PlatformAdminService", "UnsuspendUser"), () =>
    HttpResponse.json({}),
  ),
  http.post(rpc("PlatformAdminService", "ImpersonateUser"), () =>
    HttpResponse.json({ accessToken: "impersonated-token", expiresIn: "900" }),
  ),
  http.post(rpc("PlatformAdminService", "ListActiveSessions"), () =>
    HttpResponse.json({ sessions: [mockSession()] }),
  ),
  http.post(rpc("PlatformAdminService", "GetOrgEntitlements"), () =>
    HttpResponse.json({ planName: "Pro", entitlements: [] }),
  ),
  http.post(rpc("PlatformAdminService", "OverrideEntitlement"), () =>
    HttpResponse.json({ id: "override-1" }),
  ),
  http.post(rpc("PlatformAdminService", "GrantPlatformRole"), () =>
    HttpResponse.json({}),
  ),
  http.post(rpc("PlatformAdminService", "RevokePlatformRole"), () =>
    HttpResponse.json({}),
  ),
  http.post(rpc("PlatformAdminService", "ListPlatformAdmins"), () =>
    HttpResponse.json({ admins: [mockPlatformAdmin()] }),
  ),
  http.post(rpc("PlatformAdminService", "ListFeatureFlags"), () =>
    HttpResponse.json({ flags: [mockFeatureFlag()] }),
  ),
  http.post(rpc("PlatformAdminService", "UpsertFeatureFlag"), () =>
    HttpResponse.json({ name: "test-flag" }),
  ),
];
