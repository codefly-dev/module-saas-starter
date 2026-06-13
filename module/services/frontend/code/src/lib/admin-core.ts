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
    platformRole: (payload.pr ?? payload.platform_role) as PlatformRole | undefined,
    orgRole: (payload.or ?? payload.org_role) as OrgRole | undefined,
  };
}

export function detectImpersonation(accessToken: string): ImpersonationInfo {
  const payload = decodeJWTPayload(accessToken);
  // The api mints a custom flat `acting` claim holding the impersonated
  // (target) user id; the token subject (`sub`) is the admin actor. The old
  // code only looked at RFC 8693 `act.sub` / `impersonated_by`, neither of
  // which the backend emits — so the banner never rendered. Accept all three.
  const acting = payload.acting as string | undefined;
  const act = payload.act as { sub?: string } | undefined;
  const impersonatedBy = payload.impersonated_by as string | undefined;
  const sub = payload.sub as string | undefined;

  const isImpersonating = !!acting || !!act?.sub || !!impersonatedBy;
  // The impersonator is the actor: the token subject when the backend's flat
  // `acting` claim is in use, else the RFC/legacy actor fields.
  const impersonatorId = (acting ? sub : undefined) ?? act?.sub ?? impersonatedBy;
  return {
    isImpersonating,
    impersonatorId,
  };
}

const REFRESH_TOKEN_KEY = "codefly_refresh_token";
const USER_EMAIL_KEY = "codefly_user_email";

// The refresh token now lives ONLY in an httpOnly `codefly_rt` cookie set by
// the api (see rest_extras.go). JS — and therefore any XSS — can no longer read
// it. These helpers are kept as no-ops / cookie-aware shims so callers don't
// need to change shape, and we proactively purge any token left in localStorage
// by an older build.
export function getStoredRefreshToken(): string | null {
  // The httpOnly cookie is not readable from JS; the browser sends it
  // automatically on the credentialed /v1/auth/refresh request.
  return null;
}

export function storeRefreshToken(_token: string): void {
  // No-op: the backend sets the httpOnly cookie. Make sure nothing lingers in
  // localStorage from a previous (insecure) build.
  if (typeof window !== "undefined") {
    localStorage.removeItem(REFRESH_TOKEN_KEY);
  }
}

export function clearRefreshToken(): void {
  if (typeof window === "undefined") return;
  localStorage.removeItem(REFRESH_TOKEN_KEY);
  localStorage.removeItem(USER_EMAIL_KEY);
}

// Persist the user's email at login so the post-reload refresh-token
// flow can restore it. The JWT carries the user's UUID but not email
// (PII tradeoff), and the refresh-token response is just the token
// pair — no user details. Without this, the sidebar footer renders
// the raw UUID after every page reload.
export function getStoredUserEmail(): string | null {
  if (typeof window === "undefined") return null;
  return localStorage.getItem(USER_EMAIL_KEY);
}

export function storeUserEmail(email: string): void {
  if (typeof window === "undefined") return;
  localStorage.setItem(USER_EMAIL_KEY, email);
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
