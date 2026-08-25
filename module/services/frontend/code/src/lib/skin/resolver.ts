import {
	type FrontendAppearance,
	type FrontendAppearanceDefinition,
	type FrontendBranding,
	resolveFrontendAppearance,
} from "@codefly/saas-plugin-contract";
import { sourcesFromEnv } from "./sources";
import type {
	RawBrandingOverride,
	ResolvedSkin,
	ResolvedSkinBase,
	SkinKey,
	SkinSource,
} from "./types";

const CACHE_TTL_MS = 30_000;
// Cache keys are request Host headers — attacker-controllable and unbounded in
// cardinality. Cap the map so a flood of distinct hosts cannot grow it without
// bound; entries are also dropped as they expire (see resolveSkin).
export const CACHE_MAX_ENTRIES = 512;
const cache = new Map<string, { skin: ResolvedSkin; expires: number }>();

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

export interface ResolveSkinOptions {
	/** Compiled default skin; used verbatim when no source overrides it. */
	fallback: ResolvedSkinBase;
	host?: string | null;
	/** Injectable for tests; defaults to the env-configured sources. */
	sources?: SkinSource[];
	now?: () => number;
}

/**
 * Resolve the skin for a request at SSR: the first configured source that
 * returns a valid descriptor wins; anything invalid is logged and skipped so
 * the compiled default always renders. Results are cached per host for a short
 * TTL to keep per-request latency off the critical path.
 */
export async function resolveSkin(
	opts: ResolveSkinOptions,
): Promise<ResolvedSkin> {
	const sources = opts.sources ?? sourcesFromEnv();
	const host = opts.host ?? null;
	const now = opts.now ?? (() => Date.now());

	if (sources.length === 0) return { ...opts.fallback, source: "default" };

	const cacheKey = host ?? "*";
	const cached = cache.get(cacheKey);
	if (cached) {
		if (cached.expires > now()) return cached.skin;
		// Expired: drop it now rather than leaving dead entries to accumulate.
		cache.delete(cacheKey);
	}

	const key: SkinKey = { host };
	let resolved: ResolvedSkin = { ...opts.fallback, source: "default" };

	for (const source of sources) {
		let descriptor: Awaited<ReturnType<SkinSource["load"]>>;
		try {
			descriptor = await source.load(key);
		} catch {
			continue;
		}
		if (!descriptor) continue;
		try {
			// The contract validator is the single injection gate: unsafe CSS,
			// unknown fields, and out-of-range values all throw here.
			const appearance = mergeAppearance(
				opts.fallback.appearance,
				descriptor.appearance,
			);
			const branding = mergeBranding(opts.fallback.branding, descriptor.branding);
			resolved = { appearance, branding, source: source.name };
			break;
		} catch (error) {
			console.warn(
				`[skin] descriptor from '${source.name}' rejected for host=${host ?? "*"}:`,
				error,
			);
		}
	}

	setCache(cacheKey, resolved, now() + CACHE_TTL_MS);
	return resolved;
}

/** Test/ops helper: drop the in-memory resolution cache. */
export function clearSkinCache(): void {
	cache.clear();
}

function setCache(key: string, skin: ResolvedSkin, expires: number): void {
	// Bound the map: evict the oldest entry (Map preserves insertion order)
	// before inserting a new key so cardinality can never exceed the cap.
	if (!cache.has(key) && cache.size >= CACHE_MAX_ENTRIES) {
		const oldest = cache.keys().next().value;
		if (oldest !== undefined) cache.delete(oldest);
	}
	cache.set(key, { skin, expires });
}

/**
 * Overlay a validated appearance override onto the compiled fallback so tokens
 * the descriptor does NOT specify keep the compiled appearance — not the bare
 * contract default. `resolveFrontendAppearance` resolves against the contract
 * default, so validating the override on its own and then re-resolving the
 * fallback-merged definition is what preserves the compiled tokens. Both calls
 * are injection gates: an unsafe value throws in either.
 */
function mergeAppearance(
	fallback: FrontendAppearance,
	override: FrontendAppearanceDefinition | undefined,
): FrontendAppearance {
	if (override === undefined) return fallback;
	// Validate the raw override in isolation (rejects null/array/unknown-field/
	// unsafe-value descriptors exactly as before merging changed behaviour).
	resolveFrontendAppearance(override);
	return resolveFrontendAppearance({
		...fallback,
		...override,
		light: { ...fallback.light, ...(override.light ?? {}) },
		dark: { ...fallback.dark, ...(override.dark ?? {}) },
	});
}

function mergeBranding(
	base: FrontendBranding,
	override: RawBrandingOverride | undefined,
): FrontendBranding {
	if (!override) return base;
	const logoSrc = override.logo && safeAsset(override.logo.lightSrc);
	const logo = logoSrc
		? {
				lightSrc: logoSrc,
				darkSrc: safeAsset(override.logo?.darkSrc),
				alt: safeText(override.logo?.alt) ?? base.name,
			}
		: base.logo;
	return {
		name: safeText(override.name) ?? base.name,
		mark: safeText(override.mark) ?? base.mark,
		title: safeText(override.title) ?? base.title,
		description: safeText(override.description) ?? base.description,
		favicon: safeAsset(override.favicon) ?? base.favicon,
		logo,
	};
}

/** Root-relative path or https:// URL only — never a data:/http:/protocol-relative asset. */
function safeAsset(value: string | undefined): string | undefined {
	if (!value) return undefined;
	if (value.startsWith("/") && !value.startsWith("//")) return value;
	try {
		return new URL(value).protocol === "https:" ? value : undefined;
	} catch {
		return undefined;
	}
}

function safeText(value: string | undefined): string | undefined {
	if (typeof value !== "string") return undefined;
	const trimmed = value.trim();
	return trimmed.length > 0 && trimmed.length <= 200 ? trimmed : undefined;
}
