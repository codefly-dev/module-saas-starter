// Pure domain types for platform admin features. No React imports.

export interface PlatformAdmin {
  userId: string;
  platformRole: string;
  grantedBy: string;
  grantedAt?: string;
}

export interface FeatureFlag {
  name: string;
  description: string;
  enabled: boolean;
  rolloutPercent: number;
  targetOrgIds: string[];
}

export interface SessionInfo {
  id: string;
  userId: string;
  ipAddress: string;
  deviceInfo: Record<string, string>;
  createdAt?: string;
  lastActiveAt?: string;
  expiresAt?: string;
}

export interface Entitlement {
  feature: string;
  limit: bigint;
  used: bigint;
  hasOverride: boolean;
}

export interface OrgEntitlements {
  planName: string;
  entitlements: Entitlement[];
}

export const PLATFORM_ROLES = ["super_admin", "billing", "support"] as const;
export type PlatformRoleValue = (typeof PLATFORM_ROLES)[number];
