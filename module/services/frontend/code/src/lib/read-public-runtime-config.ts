import "server-only";

import { getWorkspaceConfiguration, getWorkspaceValue } from "codefly";
import { connection } from "next/server";

import {
	type PublicRuntimeConfig,
	resolvePublicRuntimeConfig,
} from "./public-runtime-config";

/**
 * Keys a restricted render refuses as plain configuration because their name
 * reads as a credential, so an operator has to provision them in the group's
 * secret half. Each is public by design — the browser receives it either way —
 * so reading the secret namespace for it discloses nothing. No other key of any
 * group is ever read from the secret namespace here.
 */
const PUBLIC_BY_DESIGN_SECRET_NAMED = new Set([
	"error-tracking/NEXT_PUBLIC_SENTRY_DSN",
]);

/** One key of one group, as the running process holds it. */
export function readWorkspaceKey(
	group: string,
	key: string,
): string | undefined {
	return PUBLIC_BY_DESIGN_SECRET_NAMED.has(`${group}/${key}`)
		? getWorkspaceValue(group, key)
		: getWorkspaceConfiguration(group, key);
}

/**
 * The browser-safe configuration of the running deployment. Awaiting
 * `connection()` keeps the read at request time: a prerendered page would freeze
 * whatever the build environment held, which for a deployed image is nothing.
 */
export async function readPublicRuntimeConfig(): Promise<PublicRuntimeConfig> {
	await connection();
	return resolvePublicRuntimeConfig(readWorkspaceKey);
}
