/**
 * Mock data factories for testing. Each factory returns a minimal valid object
 * matching the gRPC-gateway REST response shape.
 *
 * The FixtureUser type is shared with the YAML fixture loader so that
 * test data and dev-seed data use the same shape.
 */

let idCounter = 0;

function nextId(): string {
  return `test-uuid-${++idCounter}`;
}

export function resetFixtures() {
  idCounter = 0;
}

interface MockUser {
  uuid: string;
  primaryEmail: string;
  status: string;
  emailVerified: boolean;
  createdAt: string;
  updatedAt: string;
}

export function mockUser(overrides: Partial<MockUser> = {}): MockUser {
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

interface MockOrganization {
  id: string;
  name: string;
  slug: string;
  ownerId: string;
}

export function mockOrganization(overrides: Partial<MockOrganization> = {}): MockOrganization {
  return {
    id: nextId(),
    name: `Org ${idCounter}`,
    slug: `org-${idCounter}`,
    ownerId: nextId(),
    ...overrides,
  };
}

interface MockAuditEvent {
  id: string;
  actorId: string;
  actorType: string;
  action: string;
  resource: string;
  resourceId: string;
  createdAt: string;
}

export function mockAuditEvent(overrides: Partial<MockAuditEvent> = {}): MockAuditEvent {
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

interface MockInvitation {
  id: string;
  orgId: string;
  email: string;
  role: string;
  status: string;
  expiresAt: string;
  createdAt: string;
}

export function mockInvitation(overrides: Partial<MockInvitation> = {}): MockInvitation {
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

interface MockSession {
  sessionId: string;
  userId: string;
  ipAddress: string;
  createdAt: string;
  lastActiveAt: string;
}

export function mockSession(overrides: Partial<MockSession> = {}): MockSession {
  return {
    sessionId: nextId(),
    userId: nextId(),
    ipAddress: "127.0.0.1",
    createdAt: "2025-01-01T00:00:00Z",
    lastActiveAt: "2025-01-01T12:00:00Z",
    ...overrides,
  };
}

interface MockPlatformAdmin {
  userId: string;
  platformRole: string;
  grantedBy: string;
  grantedAt: string;
}

export function mockPlatformAdmin(overrides: Partial<MockPlatformAdmin> = {}): MockPlatformAdmin {
  return {
    userId: nextId(),
    platformRole: "super_admin",
    grantedBy: nextId(),
    grantedAt: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

interface MockFeatureFlag {
  name: string;
  description: string;
  enabled: boolean;
  rolloutPercent: number;
  targetOrgIds: string[];
}

export function mockFeatureFlag(overrides: Partial<MockFeatureFlag> = {}): MockFeatureFlag {
  return {
    name: `feature-${idCounter}`,
    description: `Feature flag ${idCounter}`,
    enabled: true,
    rolloutPercent: 100,
    targetOrgIds: [],
    ...overrides,
  };
}
