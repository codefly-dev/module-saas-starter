import {
	type FrontendAppearance,
	type FrontendAppearanceDefinition,
	type FrontendBranding,
	resolveFrontendAppearance,
	sanitizeFrontendAppearance,
} from "@codefly/saas-plugin-contract";
import type {
	RawBrandingOverride,
	ResolvedSkin,
	ResolvedSkinBase,
	SkinKey,
	SkinSource,
} from "./types.js";

const CACHE_TTL_MS = 30_000;
// Cache keys are request Host headers — attacker-controllable and unbounded in
// cardinality. Cap the map so a flood of distinct hosts cannot grow it without
// bound; entries are also dropped as they expire (see resolveSkin).
export const CACHE_MAX_ENTRIES = 512;
const cache = new Map<string, { skin: ResolvedSkin; expires: number }>();

// A dropped-key report is a property of the descriptor, not of the request, so
// it is emitted once per descriptor shape rather than once per resolution.
// Without that, the report sits behind the per-host cache above, whose keys are
// attacker-controllable Host headers: every distinct header misses the cache,
// re-resolves the same mounted descriptor, and re-emits the same error line, so
// a skin that is merely stale becomes one error-level log record per request.
// Bounded and evicted like the cache for the same reason.
export const REPORTED_DROPS_MAX_ENTRIES = 64;
const reportedDrops = new Set<string>();
// Key names come from an untrusted descriptor (`RawSkinDescriptor`), so a report
// caps how many it names and how long each may be. Unbounded, a descriptor with
// 50k unknown keys emits a single ~390KB log line.
const MAX_REPORTED_KEYS = 20;
const MAX_REPORTED_KEY_LENGTH = 64;

export interface ResolveSkinOptions {
	/** Compiled default skin; used verbatim when no source overrides it. */
	fallback: ResolvedSkinBase;
	host?: string | null;
	/**
	 * The delivery sources to consult, in priority order. The host wires these
	 * from its environment; the resolver itself stays free of env and Node APIs
	 * so it runs identically in the host, a remote, or a test. Required — pass an
	 * empty array to mean "no sources, use the compiled default" explicitly, so
	 * forgetting to wire sources is a compile error, not a silent default skin.
	 */
	sources: SkinSource[];
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
	const sources = opts.sources;
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
			// Unrecognised keys are dropped and named rather than sinking the whole
			// descriptor: a stale key is usually a skin written against a newer or
			// older token vocabulary, and discarding the other tokens over it renders
			// the stock default in place of a customer's brand. The injection gate is
			// unchanged — every surviving value still goes through the contract
			// validator below, which throws on unsafe CSS and out-of-range values.
			const { definition, dropped } = sanitizeFrontendAppearance(
				descriptor.appearance,
			);
			const appearance = mergeAppearance(opts.fallback.appearance, definition);
			const branding = mergeBranding(
				opts.fallback.branding,
				descriptor.branding,
			);
			resolved = { appearance, branding, source: source.name };
			// Reported only now, because only now is it true. mergeAppearance throws
			// on an unsafe or out-of-range value, and a descriptor rejected there is
			// discarded whole — reporting the dropped keys before it ran would claim
			// the surviving tokens had been applied to a request that went on to
			// render the compiled default instead.
			if (dropped.length > 0) reportDroppedKeys(source.name, host, dropped);
			break;
		} catch (error) {
			console.error(
				`[skin] descriptor from '${source.name}' rejected for host=${host ?? "*"}, rendering the compiled default instead:`,
				error,
			);
		}
	}

	setCache(cacheKey, resolved, now() + CACHE_TTL_MS);
	return resolved;
}

/**
 * Test/ops helper: drop the in-memory resolution cache. Clears the dropped-key
 * report ledger with it — that ledger suppresses repeat reports, so leaving it
 * populated across a cache clear would silence the next report of a shape
 * already seen.
 */
export function clearSkinCache(): void {
	cache.clear();
	reportedDrops.clear();
}

/**
 * Name the appearance keys a descriptor declared that this contract does not
 * define, once per descriptor shape.
 *
 * Both caps here are about untrusted input rather than tidiness: the key names
 * are descriptor-controlled, so they are stripped of control characters (a
 * newline in a key name otherwise forges a whole log record) and truncated, and
 * only the first `MAX_REPORTED_KEYS` are named with the rest summarised.
 */
function reportDroppedKeys(
	sourceName: string,
	host: string | null,
	dropped: readonly string[],
): void {
	const named = dropped.slice(0, MAX_REPORTED_KEYS).map(safeKeyName);
	// The signature is built from the capped names, never the raw array, so the
	// ledger cannot retain an oversized descriptor's key list.
	const signature = `${sourceName}\u0000${dropped.length}\u0000${named.join("\u0000")}`;
	if (reportedDrops.has(signature)) return;
	if (reportedDrops.size >= REPORTED_DROPS_MAX_ENTRIES) {
		const oldest = reportedDrops.values().next().value;
		if (oldest !== undefined) reportedDrops.delete(oldest);
	}
	reportedDrops.add(signature);
	const remaining = dropped.length - named.length;
	const list =
		remaining > 0
			? `${named.join(", ")} (+${remaining} more)`
			: named.join(", ");
	console.error(
		`[skin] descriptor from '${sourceName}' for host=${host ?? "*"} declares ${dropped.length} unknown appearance key(s), ignored: ${list}. The remaining tokens were applied; update the descriptor or the token contract so the intended values take effect. Reported once per descriptor shape.`,
	);
}

/** Render one descriptor-controlled key name safely into a single log record. */
function safeKeyName(key: string): string {
	const printable = key.replace(/[\p{Cc}\p{Cf}]/gu, "\uFFFD");
	return printable.length > MAX_REPORTED_KEY_LENGTH
		? `${printable.slice(0, MAX_REPORTED_KEY_LENGTH)}…`
		: printable;
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
	override: unknown,
): FrontendAppearance {
	if (override === undefined) return fallback;
	// Validate the raw override in isolation (rejects null/array/unknown-field/
	// unsafe-value descriptors exactly as before merging changed behaviour).
	// This call is also the type guard: nothing that is not a well-formed
	// definition survives it, so the assertion below rests on a runtime check
	// made one line earlier, not on trusting the caller.
	resolveFrontendAppearance(override as FrontendAppearanceDefinition);
	const definition = override as FrontendAppearanceDefinition;
	return resolveFrontendAppearance({
		...fallback,
		...definition,
		light: { ...fallback.light, ...(definition.light ?? {}) },
		dark: { ...fallback.dark, ...(definition.dark ?? {}) },
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
