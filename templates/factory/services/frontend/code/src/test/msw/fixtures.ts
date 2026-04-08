/**
 * Mock data factories for testing. Each factory returns a minimal valid object
 * matching the gRPC-gateway REST response shape.
 */

let idCounter = 0;

function nextId(): string {
  return `test-uuid-${++idCounter}`;
}

export function resetFixtures() {
  idCounter = 0;
}

export function mockUser(overrides: Record<string, unknown> = {}) {
  return {
    uuid: nextId(),
    primaryEmail: `user-${idCounter}@test.com`,
    status: "USER_STATUS_ACTIVE",
    emailVerified: true,
    createdAt: "2025-01-01T00:00:00Z",
    updatedAt: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

export function mockOrganization(overrides: Record<string, unknown> = {}) {
  return {
    id: nextId(),
    name: `Org ${idCounter}`,
    slug: `org-${idCounter}`,
    ownerId: nextId(),
    ...overrides,
  };
}

export function mockAuditEvent(overrides: Record<string, unknown> = {}) {
  return {
    id: nextId(),
    actorId: nextId(),
    actorType: "user",
    action: "user.registered",
    resource: "user",
    resourceId: nextId(),
    createdAt: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

export function mockInvitation(overrides: Record<string, unknown> = {}) {
  return {
    id: nextId(),
    orgId: nextId(),
    email: `invite-${idCounter}@test.com`,
    role: "member",
    status: "pending",
    expiresAt: "2025-01-08T00:00:00Z",
    createdAt: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

export function mockSession(overrides: Record<string, unknown> = {}) {
  return {
    sessionId: nextId(),
    userId: nextId(),
    ipAddress: "127.0.0.1",
    createdAt: "2025-01-01T00:00:00Z",
    lastActiveAt: "2025-01-01T12:00:00Z",
    ...overrides,
  };
}

export function mockPlatformAdmin(overrides: Record<string, unknown> = {}) {
  return {
    userId: nextId(),
    platformRole: "super_admin",
    grantedBy: nextId(),
    grantedAt: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

export function mockFeatureFlag(overrides: Record<string, unknown> = {}) {
  return {
    name: `feature-${idCounter}`,
    description: `Feature flag ${idCounter}`,
    enabled: true,
    rolloutPercent: 100,
    targetOrgIds: [],
    ...overrides,
  };
}
