import "server-only";

import { getWorkspaceConfiguration } from "codefly";
import { connection } from "next/server";

import type { IdentityConfig } from "./auth";

/**
 * The browser-safe half of the Codefly `identity` workspace configuration, read
 * from the running process. Awaiting `connection()` keeps the read at request
 * time: a prerendered page would freeze whatever the build environment held,
 * which for a deployed image is nothing. Secret keys of the group are never
 * read here.
 */
export async function readIdentityConfig(): Promise<IdentityConfig> {
	await connection();
	const value = (key: string) => getWorkspaceConfiguration("identity", key);
	return {
		provider: value("IDENTITY_PROVIDER"),
		authorizeURL: value("IDENTITY_AUTHORIZE_URL"),
		clientID: value("IDENTITY_CLIENT_ID"),
		displayName: value("IDENTITY_DISPLAY_NAME"),
		scope: value("IDENTITY_SCOPE"),
		authorizeSelector: value("IDENTITY_AUTHORIZE_SELECTOR"),
	};
}
