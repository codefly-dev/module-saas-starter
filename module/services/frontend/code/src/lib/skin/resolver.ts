import {
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
const cache = new Map<string, { skin: ResolvedSkin; expires: number }>();

/**
 * True when at least one runtime skin source is configured. The render path
 * checks this before reading request headers, so an unconfigured deployment
 * stays on the compiled default skin and keeps static rendering.
 */
export function skinResolutionEnabled(
	env: NodeJS.ProcessEnv = process.env,
): boolean {
	return sourcesFromEnv(env).length > 0;
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
	if (cached && cached.expires > now()) return cached.skin;

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
			const appearance = resolveFrontendAppearance(descriptor.appearance);
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

	cache.set(cacheKey, { skin: resolved, expires: now() + CACHE_TTL_MS });
	return resolved;
}

/** Test/ops helper: drop the in-memory resolution cache. */
export function clearSkinCache(): void {
	cache.clear();
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
