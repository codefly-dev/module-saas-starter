// Pure domain types for API keys. No React imports.

export interface APIKeyPermission {
  resource: string;
  action: string;
}

export interface APIKey {
  id: string;
  organizationId: string;
  userId: string;
  name: string;
  prefix: string;
  scopes: APIKeyPermission[];
  environment: number;
  createdAt?: string;
  expiresAt?: string;
  lastUsedAt?: string;
  revokedAt?: string;
}

export type APIKeyEnvironmentLabel = "live" | "test" | "unknown";
