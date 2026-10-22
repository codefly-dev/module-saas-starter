import { messageOf } from "./errors.js";
import type { AccountLinkView, DatasourceView } from "./types.js";

/**
 * An API a surface connects a person to: the base URL it fixes, and what to
 * call it. Where this comes from — a solution's manifest, a module's
 * declaration, a constant — is the caller's to decide; nothing here reads one.
 *
 * Like a declared GitHub source, the API is not a question put to the person.
 * They supply only the sign-in.
 */
export interface ApiSourceSpec {
	/** Absolute http(s) base URL, e.g. "https://api.example.com". */
	baseUrl: string;
	/** A path under `baseUrl` to pull. Absent for a source that is only called. */
	resourcePath?: string;
	/**
	 * What a person calls the API, e.g. "Example CRM". Absent falls back to the
	 * base URL's host.
	 */
	name?: string;
	/**
	 * The collection the source writes into, minted or reused by label. Absent
	 * falls back to the name. Ignored when `boundaryNodeId` is given.
	 */
	collection?: string;
	/** An existing scope node for the source to write into. */
	boundaryNodeId?: string;
}

/** What a person reads the API as. */
export function apiSourceName(spec: ApiSourceSpec): string {
	const name = spec.name?.trim();
	if (name) return name;
	try {
		return new URL(spec.baseUrl).host || spec.baseUrl;
	} catch {
		return spec.baseUrl;
	}
}

/** The collection the person's source writes into. */
export function apiSourceCollection(spec: ApiSourceSpec): string {
	return spec.collection?.trim() || apiSourceName(spec);
}

/**
 * Every source of the caller's that serves the API: an `api` source holding
 * the authorization-code grant, at the same base URL and resource path. The
 * host trims both and changes nothing else about them, so they are compared
 * trimmed and otherwise exactly — a trailing slash is a different base path.
 *
 * Only the caller's own are ever in the list: the host leaves another member's
 * personal source out of `ListSources`, and a source with this grant is always
 * personal. Returns all of them, as `matchDeclaredSources` does, so a caller
 * reports two rather than silently picking one.
 */
export function matchPersonalApiSources(
	spec: ApiSourceSpec,
	sources: readonly DatasourceView[],
): DatasourceView[] {
	const baseUrl = spec.baseUrl.trim();
	const resourcePath = (spec.resourcePath ?? "").trim();
	return sources.filter(
		(source) =>
			source.provider === "api" &&
			source.api?.credentialKind === "oauth2" &&
			source.api.oauth2Grant === "authorization_code" &&
			source.api.baseUrl.trim() === baseUrl &&
			source.api.resourcePath.trim() === resourcePath,
	);
}

/**
 * The provider account a generic OAuth link records for a source. That sign-in
 * proves only that the person authorized the source, not who they are at the
 * provider, so the host names the link after the source it authorized
 * (`module/CALLABLE_SOURCES.md`, OAuth and disclosure).
 */
export function sourceLinkAccountId(sourceId: string): string {
	return `source:${sourceId}`;
}

/** The person's link for one source, if they have signed in for it. */
export function sourceLinks(
	links: readonly AccountLinkView[],
	sourceId: string,
): AccountLinkView[] {
	const account = sourceLinkAccountId(sourceId);
	return links.filter(
		(link) => link.connector === "api" && link.providerAccountId === account,
	);
}

/**
 * The host's refusals on the way to connecting an API, in words that say what
 * to do. The host writes each of these from a fixed message, so they are
 * matched on it; anything else is shown in the host's own words.
 */
const refusals: { match: RegExp; words: (name: string) => string }[] = [
	{
		// AddSource, and BeginDatasourceAccountLink for the `api` connector.
		match:
			/provider sign-in is not configured|cannot link api accounts|no sign-in with that provider is configured/i,
		words: (name) =>
			`This deployment has no sign-in configured for ${name}, so it cannot be connected yet. Whoever operates the deployment registers it, in the datasource-oauth configuration.`,
	},
	{
		// AddSource and DeclareSourceOperations take an administrator.
		match: /requires org admin or owner role|organization administrator required/i,
		words: (name) =>
			`Connecting ${name} in this organization needs the administrator or owner role, which you do not hold here.`,
	},
	{
		match: /source permission required/i,
		words: () =>
			"You do not have access to the collection this source writes into. Ask an organization administrator to grant it to you.",
	},
	{
		match: /source is personal to another member|source is not owned by this member/i,
		words: () =>
			"This source belongs to another member of the organization. Connect your own instead.",
	},
	{
		match: /not a member of this organization|organization membership required/i,
		words: () => "You are not a member of this organization.",
	},
];

export function apiSourceRefusal(error: unknown, name: string): string {
	const message = messageOf(error);
	const refusal = refusals.find(({ match }) => match.test(message));
	return refusal ? refusal.words(name) : message;
}
