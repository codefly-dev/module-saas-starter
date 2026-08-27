import type { RawSkinDescriptor, SkinSource } from "@codefly/ui/skin";

/**
 * The delivery mechanisms, each behind the one `SkinSource` seam. They are
 * wired from environment configuration only, so an unconfigured deployment
 * keeps using the compiled default skin with zero runtime cost.
 * `sourcesFromEnv` orders them by specificity:
 *
 *   1. file — a mounted ConfigMap directory (per-host `<host>.json`, else `default.json`).
 *   2. env  — a single skin descriptor as a JSON blob in an env var (simplest).
 *
 * The first source that returns a descriptor wins.
 */
export function sourcesFromEnv(
	env: NodeJS.ProcessEnv = process.env,
): SkinSource[] {
	return [fileSkinSource(env), envSkinSource(env)].filter(
		(source): source is SkinSource => source !== null,
	);
}

/**
 * True when at least one runtime skin source is configured. The render path
 * reads the request Host header only when this (or the build-time flag) is
 * true — see `shouldResolveHost` — so an unconfigured deployment stays on the
 * compiled default skin and never pays for header access.
 */
export function skinResolutionEnabled(
	env: NodeJS.ProcessEnv = process.env,
): boolean {
	return sourcesFromEnv(env).length > 0;
}

/**
 * Whether the render path should read the request Host header. Reading headers
 * is what opts a route into dynamic rendering, and that decision has to hold at
 * two different times:
 *
 *   - BUILD: a skinnable image is built before any skin is mounted, so no source
 *     is configured yet. The build-time flag FRONTEND_SKIN_RUNTIME=1 forces the
 *     header read (hence dynamic rendering) so the image doesn't prerender the
 *     compiled default.
 *   - RUNTIME: the flag is build-only and is NOT inlined into the server bundle,
 *     so at request time it is absent. Source presence (`skinResolutionEnabled`,
 *     driven by the runtime-mounted FRONTEND_SKIN_DIR) is what keeps per-host
 *     resolution actually running.
 *
 * Gating on the flag alone would read the host at build but never at runtime,
 * silently collapsing every per-host skin to the default.
 */
export function shouldResolveHost(
	env: NodeJS.ProcessEnv = process.env,
): boolean {
	return env.FRONTEND_SKIN_RUNTIME === "1" || skinResolutionEnabled(env);
}

/** A single skin descriptor as a JSON blob in `FRONTEND_SKIN_JSON`. */
export function envSkinSource(
	env: NodeJS.ProcessEnv = process.env,
): SkinSource | null {
	const single = env.FRONTEND_SKIN_JSON;
	if (!single) return null;
	return {
		name: "env",
		async load() {
			const parsed = safeParse(single);
			return isRecord(parsed) ? (parsed as RawSkinDescriptor) : null;
		},
	};
}

/** A mounted ConfigMap directory (`FRONTEND_SKIN_DIR`) with `<host>.json` / `default.json`. */
export function fileSkinSource(
	env: NodeJS.ProcessEnv = process.env,
): SkinSource | null {
	const dir = env.FRONTEND_SKIN_DIR;
	if (!dir) return null;
	return {
		name: "file",
		async load(key) {
			// Imported lazily so the Node-only fs API never reaches an edge/client bundle.
			const { readFile } = await import("node:fs/promises");
			const { join } = await import("node:path");
			const candidates = [
				key.host ? `${sanitizeHost(key.host)}.json` : null,
				"default.json",
			].filter((name): name is string => name !== null);
			for (const file of candidates) {
				try {
					const parsed = safeParse(await readFile(join(dir, file), "utf8"));
					if (isRecord(parsed)) return parsed as RawSkinDescriptor;
				} catch {
					// Missing/unreadable file — try the next candidate.
				}
			}
			return null;
		},
	};
}

function safeParse(text: string): unknown {
	try {
		return JSON.parse(text);
	} catch {
		return null;
	}
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Keep a host safe as a filename segment (defence in depth against traversal). */
function sanitizeHost(host: string): string {
	return host.replace(/[^a-z0-9.-]/gi, "_");
}
