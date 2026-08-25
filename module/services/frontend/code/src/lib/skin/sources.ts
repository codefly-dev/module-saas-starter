import type { RawSkinDescriptor, SkinSource } from "./types";

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
export function sourcesFromEnv(env: NodeJS.ProcessEnv = process.env): SkinSource[] {
	return [fileSkinSource(env), envSkinSource(env)].filter(
		(source): source is SkinSource => source !== null,
	);
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
