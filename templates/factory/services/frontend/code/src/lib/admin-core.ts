/**
 * admin-core — Pure TS, zero React. Plugin types, config builder,
 * auth utilities, and transforms.
 */

// ============================================================================
// Plugin Types
// ============================================================================

export type PlatformRole = "super_admin" | "billing" | "support";
export type OrgRole = "owner" | "admin" | "member";

export interface NavItem {
  label: string;
  href: string;
  icon?: string;
  group?: string;
  requiredRole?: OrgRole | PlatformRole;
}

export interface ColumnDefinition {
  key: string;
  label: string;
  sortable?: boolean;
}

export interface Resource {
  name: string;
  label: { singular: string; plural: string };
  columns: ColumnDefinition[];
  searchable?: boolean;
}

export interface DashboardWidget {
  id: string;
  component: React.LazyExoticComponent<React.ComponentType>;
  priority?: number;
}

export interface AdminPlugin {
  name: string;
  navItems?: NavItem[];
  resources?: Resource[];
  widgets?: DashboardWidget[];
}

export interface AdminConfig {
  plugins: AdminPlugin[];
  navItems: NavItem[];
  resources: Resource[];
  widgets: DashboardWidget[];
}

// ============================================================================
// Config Builder
// ============================================================================

export function buildAdminConfig(plugins: AdminPlugin[]): AdminConfig {
  return {
    plugins,
    navItems: plugins.flatMap((p) => p.navItems ?? []),
    resources: plugins.flatMap((p) => p.resources ?? []),
    widgets: plugins
      .flatMap((p) => p.widgets ?? [])
      .sort((a, b) => (a.priority ?? 0) - (b.priority ?? 0)),
  };
}

// ============================================================================
// Auth Utilities (pure functions, no React)
// ============================================================================

export interface ImpersonationInfo {
  isImpersonating: boolean;
  impersonatorId?: string;
}

export function decodeJWTPayload(token: string): Record<string, unknown> {
  try {
    const payload = token.split(".")[1];
    return JSON.parse(atob(payload));
  } catch {
    return {};
  }
}

export function extractRoles(accessToken: string): {
  platformRole?: PlatformRole;
  orgRole?: OrgRole;
} {
  const payload = decodeJWTPayload(accessToken);
  return {
    platformRole: payload.platform_role as PlatformRole | undefined,
    orgRole: payload.org_role as OrgRole | undefined,
  };
}

export function detectImpersonation(accessToken: string): ImpersonationInfo {
  const payload = decodeJWTPayload(accessToken);
  // RFC 8693 `act` claim or custom `impersonated_by`
  const act = payload.act as { sub?: string } | undefined;
  const impersonatedBy = payload.impersonated_by as string | undefined;
  const impersonatorId = act?.sub ?? impersonatedBy;
  return {
    isImpersonating: !!impersonatorId,
    impersonatorId,
  };
}

const REFRESH_TOKEN_KEY = "codefly_refresh_token";

export function getStoredRefreshToken(): string | null {
  if (typeof window === "undefined") return null;
  return localStorage.getItem(REFRESH_TOKEN_KEY);
}

export function storeRefreshToken(token: string): void {
  if (typeof window === "undefined") return;
  localStorage.setItem(REFRESH_TOKEN_KEY, token);
}

export function clearRefreshToken(): void {
  if (typeof window === "undefined") return;
  localStorage.removeItem(REFRESH_TOKEN_KEY);
}

// ============================================================================
// Transforms (pure functions)
// ============================================================================

export function formatDate(dateString: string | undefined): string {
  if (!dateString) return "-";
  try {
    return new Intl.DateTimeFormat("en-US", {
      dateStyle: "medium",
      timeStyle: "short",
    }).format(new Date(dateString));
  } catch {
    return dateString;
  }
}

export function truncateUUID(uuid: string): string {
  if (!uuid) return "-";
  return uuid.slice(0, 8) + "...";
}

export function formatLimit(limit: number): string {
  if (limit < 0) return "Unlimited";
  if (limit === 0) return "Disabled";
  return limit.toLocaleString();
}
