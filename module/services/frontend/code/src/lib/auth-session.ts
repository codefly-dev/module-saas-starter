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
const USER_NAME_KEY = "codefly_user_name";

export interface SessionUser {
	id: string;
	email?: string;
	name?: string;
}

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
	localStorage.removeItem(USER_NAME_KEY);
}

export function getStoredUserEmail(): string | null {
	if (typeof window === "undefined") return null;
	return localStorage.getItem(USER_EMAIL_KEY);
}

export function storeUserEmail(email: string): void {
	if (typeof window === "undefined") return;
	localStorage.setItem(USER_EMAIL_KEY, email);
}

export function getStoredUserName(): string | null {
	if (typeof window === "undefined") return null;
	return localStorage.getItem(USER_NAME_KEY);
}

export function storeUserName(name: string): void {
	if (typeof window === "undefined") return;
	localStorage.setItem(USER_NAME_KEY, name);
}

// resolveSessionUser derives the presentational identity for a session. Email
// and name each resolve by the same precedence: an explicit value from the
// login response > the JWT claim minted by accounts > a value persisted on a
// previous login (surviving a refresh-token round-trip after page reload). The
// id is only ever the raw subject and is the last-resort label.
export function resolveSessionUser(
	accessToken: string,
	overrides: { userId?: string; email?: string; name?: string } = {},
): SessionUser {
	const payload = decodeJWTPayload(accessToken);
	const email =
		overrides.email ||
		(typeof payload.email === "string" ? payload.email : undefined) ||
		getStoredUserEmail() ||
		undefined;
	const name =
		overrides.name ||
		(typeof payload.name === "string" ? payload.name : undefined) ||
		getStoredUserName() ||
		undefined;
	return {
		id: overrides.userId || String(payload.sub ?? ""),
		email,
		name,
	};
}

// sessionDisplayLabel is the human-facing label for a signed-in user: the name
// when known, otherwise the email, and only the raw id when neither exists.
export function sessionDisplayLabel(user: SessionUser | null): string {
	if (!user) return "";
	return user.name || user.email || user.id;
}
