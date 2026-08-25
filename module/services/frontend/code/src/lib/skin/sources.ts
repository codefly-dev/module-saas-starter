import type { RawSkinDescriptor, SkinSource } from "./types";

/**
 * The three delivery mechanisms under evaluation, each behind the one
 * `SkinSource` seam. They are wired from environment configuration only, so an
 * unconfigured deployment keeps using the compiled default skin with zero
 * runtime cost. `sourcesFromEnv` orders them by liveness:
 *
 *   1. HTTP  — a CMS / config service resolving skins per host (live, no redeploy).
 *   2. file  — a mounted ConfigMap directory (per-deployment, hot-reloadable).
 *   3. env   — a JSON blob or host→descriptor map in an env var (simplest).
 *
 * The first source that returns a descriptor wins.
 */
export function sourcesFromEnv(env: NodeJS.ProcessEnv = process.env): SkinSource[] {
	return [httpSkinSource(env), fileSkinSource(env), envSkinSource(env)].filter(
		(source): source is SkinSource => source !== null,
	);
}

/** A single skin in `FRONTEND_SKIN_JSON`, or a `{ "host": {...} }` map in `FRONTEND_SKINS_JSON`. */
export function envSkinSource(
	env: NodeJS.ProcessEnv = process.env,
): SkinSource | null {
	const single = env.FRONTEND_SKIN_JSON;
	const byHostRaw = env.FRONTEND_SKINS_JSON;
	if (!single && !byHostRaw) return null;
	return {
		name: "env",
		async load(key) {
			if (byHostRaw) {
				const map = safeParse(byHostRaw);
				if (isRecord(map)) {
					const hit =
						(key.host ? map[key.host] : undefined) ?? map["*"] ?? undefined;
					if (isRecord(hit)) return hit as RawSkinDescriptor;
				}
			}
			if (single) {
				const parsed = safeParse(single);
				if (isRecord(parsed)) return parsed as RawSkinDescriptor;
			}
			return null;
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

/** A CMS / config service (`FRONTEND_SKIN_ENDPOINT`) resolving a skin by `?host=`. */
export function httpSkinSource(
	env: NodeJS.ProcessEnv = process.env,
): SkinSource | null {
	const endpoint = env.FRONTEND_SKIN_ENDPOINT;
	if (!endpoint) return null;
	return {
		name: "http",
		async load(key) {
			let url: URL;
			try {
				url = new URL(endpoint);
			} catch {
				return null;
			}
			if (key.host) url.searchParams.set("host", key.host);
			try {
				const response = await fetch(url, {
					headers: { accept: "application/json" },
					signal: AbortSignal.timeout(2000),
				});
				if (!response.ok) return null;
				const body = await response.json();
				return isRecord(body) ? (body as RawSkinDescriptor) : null;
			} catch {
				// Timeout / network / bad JSON — fall through to the next source.
				return null;
			}
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
