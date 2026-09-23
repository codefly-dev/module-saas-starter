import "server-only";

import { getWorkspaceConfiguration } from "codefly";
import { connection } from "next/server";

import type { IdentityConfig } from "./auth";

const DISCOVERY_TIMEOUT_MS = 5_000;

// One discovery per issuer per process: the document is the provider's
// published metadata and does not change between requests.
const discovered = new Map<string, Promise<string | undefined>>();

/**
 * The provider's own authorization endpoint, read from its OIDC discovery
 * document — the same document accounts reads its token endpoint from, so the
 * two halves of the code flow always name one provider. A failed lookup is not
 * cached, so the next request retries.
 */
function discoveredAuthorizeURL(issuer: string): Promise<string | undefined> {
	const key = issuer.replace(/\/+$/, "");
	let pending = discovered.get(key);
	if (!pending) {
		pending = fetch(`${key}/.well-known/openid-configuration`, {
			signal: AbortSignal.timeout(DISCOVERY_TIMEOUT_MS),
		})
			.then(async (response) => {
				if (!response.ok) {
					throw new Error(`discovery answered ${response.status}`);
				}
				const document = (await response.json()) as {
					authorization_endpoint?: unknown;
				};
				const endpoint = document.authorization_endpoint;
				if (typeof endpoint !== "string" || !endpoint.startsWith("https://")) {
					throw new Error("discovery names no https authorization_endpoint");
				}
				return endpoint;
			})
			.catch((error: unknown) => {
				discovered.delete(key);
				console.error(`identity: cannot discover ${key}`, error);
				return undefined;
			});
		discovered.set(key, pending);
	}
	return pending;
}

/**
 * The browser-safe half of the Codefly `identity` workspace configuration, read
 * from the running process. Awaiting `connection()` keeps the read at request
 * time: a prerendered page would freeze whatever the build environment held,
 * which for a deployed image is nothing. Secret keys of the group are never
 * read here.
 *
 * IDENTITY_AUTHORIZE_URL is an optional override; without it the endpoint is
 * discovered from IDENTITY_ISSUER, so a deployment configures an issuer once.
 */
export async function readIdentityConfig(): Promise<IdentityConfig> {
	await connection();
	const value = (key: string) => getWorkspaceConfiguration("identity", key);
	const issuer = value("IDENTITY_ISSUER");
	return {
		provider: value("IDENTITY_PROVIDER"),
		authorizeURL:
			value("IDENTITY_AUTHORIZE_URL") ??
			(issuer ? await discoveredAuthorizeURL(issuer) : undefined),
		clientID: value("IDENTITY_CLIENT_ID"),
		displayName: value("IDENTITY_DISPLAY_NAME"),
		scope: value("IDENTITY_SCOPE"),
		authorizeSelector: value("IDENTITY_AUTHORIZE_SELECTOR"),
	};
}
