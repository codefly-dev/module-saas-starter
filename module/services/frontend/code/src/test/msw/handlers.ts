/**
 * MSW handlers for gRPC-gateway REST routes.
 * Each handler mirrors a proto service RPC mapped to HTTP.
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

const BASE = "http://test:8080";

export const handlers = [
  // ── UserService ───────────────────────────────────────────
  http.post(`${BASE}/v1/users`, () => {
    return HttpResponse.json({ user: mockUser(), identity: null });
  }),

  http.get(`${BASE}/v1/users/self`, () => {
    return HttpResponse.json({
      user: mockUser({ uuid: "self-user-id", primaryEmail: "me@test.com" }),
      identities: [],
      organizations: [mockOrganization()],
    });
  }),

  http.get(`${BASE}/v1/users/:uuid`, ({ params }) => {
    return HttpResponse.json(mockUser({ uuid: String(params.uuid) }));
  }),

  // ── AuthService ───────────────────────────────────────────
  http.post(`${BASE}/v1/auth/authenticate`, () => {
    return HttpResponse.json({
      accessToken: "test-access-token",
      refreshToken: "test-refresh-token",
      expiresIn: 900,
      user: mockUser({ uuid: "auth-user-id" }),
    });
  }),

  http.post(`${BASE}/v1/auth/refresh`, () => {
    return HttpResponse.json({
      accessToken: "new-access-token",
      refreshToken: "new-refresh-token",
      expiresIn: 900,
    });
  }),

  // ── OrganizationService ───────────────────────────────────
  http.get(`${BASE}/v1/organizations`, () => {
    return HttpResponse.json({
      organizations: [mockOrganization(), mockOrganization()],
    });
  }),

  http.get(`${BASE}/v1/organizations/:id/members`, () => {
    return HttpResponse.json({ members: [] });
  }),

  // ── InvitationService ─────────────────────────────────────
  http.get(`${BASE}/v1/invitations`, () => {
    return HttpResponse.json({
      invitations: [mockInvitation()],
    });
  }),

  // ── AuditService ──────────────────────────────────────────
  http.get(`${BASE}/v1/audit`, () => {
    return HttpResponse.json({
      events: [mockAuditEvent(), mockAuditEvent()],
      totalCount: 2,
    });
  }),

  // ── PlatformAdminService ──────────────────────────────────
  http.get(`${BASE}/v1/platform/users`, () => {
    return HttpResponse.json({
      users: [mockUser(), mockUser()],
      nextPageToken: "",
    });
  }),

  http.post(`${BASE}/v1/platform/users/:userId\\:suspend`, () => {
    return HttpResponse.json({});
  }),

  http.post(`${BASE}/v1/platform/users/:userId\\:unsuspend`, () => {
    return HttpResponse.json({});
  }),

  http.get(`${BASE}/v1/platform/sessions`, () => {
    return HttpResponse.json({ sessions: [mockSession()] });
  }),

  http.get(`${BASE}/v1/platform/admins`, () => {
    return HttpResponse.json({ admins: [mockPlatformAdmin()] });
  }),

  http.post(`${BASE}/v1/platform/admins`, () => {
    return HttpResponse.json({});
  }),

  http.delete(`${BASE}/v1/platform/admins/:userId`, () => {
    return HttpResponse.json({});
  }),

  http.get(`${BASE}/v1/platform/feature-flags`, () => {
    return HttpResponse.json({ flags: [mockFeatureFlag()] });
  }),

  http.put(`${BASE}/v1/platform/feature-flags/:name`, () => {
    return HttpResponse.json({ name: "test-flag" });
  }),
];
