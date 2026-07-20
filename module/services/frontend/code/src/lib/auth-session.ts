export type PlatformRole = "super_admin" | "billing" | "support";
export type OrgRole = "owner" | "admin" | "member";

export interface ImpersonationInfo {
	isImpersonating: boolean;
	impersonatorId?: string;
}

export interface SessionContext {
	organizationId?: string;
	platformRole?: PlatformRole;
	orgRole?: OrgRole;
}

export function decodeJWTPayload(token: string): Record<string, unknown> {
	try {
		const payload = token.split(".")[1];
		if (!payload) return {};
		const base64 = payload.replace(/-/g, "+").replace(/_/g, "/");
		return JSON.parse(
			atob(base64.padEnd(Math.ceil(base64.length / 4) * 4, "=")),
		);
	} catch {
		return {};
	}
}

export function extractSessionContext(accessToken: string): SessionContext {
	const payload = decodeJWTPayload(accessToken);
	const organizationId =
		typeof payload.org === "string" && payload.org ? payload.org : undefined;
	return {
		organizationId,
		platformRole: parsePlatformRole(payload.pr),
		orgRole: parseOrgRole(payload.or),
	};
}

function parsePlatformRole(value: unknown): PlatformRole | undefined {
	return value === "super_admin" || value === "billing" || value === "support"
		? value
		: undefined;
}

function parseOrgRole(value: unknown): OrgRole | undefined {
	return value === "owner" || value === "admin" || value === "member"
		? value
		: undefined;
}

export function extractRoles(
	accessToken: string,
): Pick<SessionContext, "platformRole" | "orgRole"> {
	const { platformRole, orgRole } = extractSessionContext(accessToken);
	return { platformRole, orgRole };
}

export function detectImpersonation(accessToken: string): ImpersonationInfo {
	const payload = decodeJWTPayload(accessToken);
	const acting = payload.acting as string | undefined;
	const act = payload.act as { sub?: string } | undefined;
	const impersonatedBy = payload.impersonated_by as string | undefined;
	const sub = payload.sub as string | undefined;

	return {
		isImpersonating: !!acting || !!act?.sub || !!impersonatedBy,
		impersonatorId: (acting ? sub : undefined) ?? act?.sub ?? impersonatedBy,
	};
}

const REFRESH_TOKEN_KEY = "codefly_refresh_token";
const USER_EMAIL_KEY = "codefly_user_email";

export function getStoredRefreshToken(): string | null {
	return null;
}

export function storeRefreshToken(token: string): void {
	void token;
	if (typeof window !== "undefined") localStorage.removeItem(REFRESH_TOKEN_KEY);
}

export function clearRefreshToken(): void {
	if (typeof window === "undefined") return;
	localStorage.removeItem(REFRESH_TOKEN_KEY);
	localStorage.removeItem(USER_EMAIL_KEY);
}

export function getStoredUserEmail(): string | null {
	if (typeof window === "undefined") return null;
	return localStorage.getItem(USER_EMAIL_KEY);
}

export function storeUserEmail(email: string): void {
	if (typeof window === "undefined") return;
	localStorage.setItem(USER_EMAIL_KEY, email);
}
