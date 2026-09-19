import { clearSkinCache, resolveSkin } from "./resolver.js";
import type {
	RawSkinDescriptor,
	ResolvedSkin,
	ResolvedSkinBase,
	SkinSource,
} from "./types.js";

// The authoring-time half of the skin contract.
//
// Two correct decisions compose into a silent failure: the contract validator is
// fail-CLOSED (an unknown field throws) and the resolver is fail-SAFE (it catches
// and keeps the compiled default). Together, one key a descriptor declares and the
// contract does not define costs that descriptor EVERY value it carried, and the
// only trace at runtime is a log line on a server nobody is reading.
//
// The repository that authors a descriptor is where that must be caught, and the
// check has to run the REAL resolver: a private schema in the authoring repository
// would pass while the resolver disagreed, which is the whole failure again with
// an extra step. So the contract ships the check rather than each descriptor-owning
// repository re-deriving it.

/** One declared leaf that did not survive resolution. */
export interface SkinLeafMismatch {
	/** Dotted path as written in the descriptor, e.g. `appearance.light.primary`. */
	path: string;
	/** What the descriptor declared at that path. */
	declared: string;
	/** What the resolved skin carries there — `undefined` when the leaf is gone. */
	resolved: unknown;
}

export interface SkinSurvivalReport {
	/** The resolved skin, whether or not every leaf survived. */
	skin: ResolvedSkin;
	/** Which source won. `"default"` means the descriptor was rejected outright. */
	source: string;
	/** Every declared leaf that is missing or changed, in descriptor order. */
	mismatches: SkinLeafMismatch[];
	/** True when nothing was dropped and the expected source won. */
	ok: boolean;
}

export interface SkinSurvivalOptions {
	/**
	 * The compiled default this descriptor overlays — the same fallback the host
	 * passes `resolveSkin`. Required: resolving against a different fallback than
	 * the product uses would report leaves as surviving that the product drops.
	 */
	fallback: ResolvedSkinBase;
	/**
	 * Assert which source won, so a descriptor that was rejected outright fails on
	 * the source rather than only on its leaves. Paired with `sources` it is the
	 * stronger assertion: the product can never silently BE the fallback.
	 */
	expectSource?: string;
	/**
	 * Resolve through these sources instead of wrapping the descriptor in a
	 * one-shot source. An authoring repository passes its REAL chain — the same
	 * mounted-file or env source the deployment uses — so the check proves the
	 * file was found, parsed, and applied, not merely that its content would
	 * validate if something loaded it.
	 */
	sources?: SkinSource[];
	/** Name of the one-shot source built around the descriptor. Ignored with `sources`. */
	sourceName?: string;
}

const DESCRIPTOR_SOURCE_NAME = "descriptor";

/** Top-level descriptor keys the resolver reads. Anything else is inert. */
const RESOLVED_DESCRIPTOR_KEYS = ["appearance", "branding"];

/**
 * Resolve a descriptor through the real resolver and report every declared leaf
 * that did not make it into the rendered skin.
 *
 * The walk is over the DESCRIPTOR, not the resolved skin: a skin is partial by
 * design, so the resolved side always carries more than the descriptor declared,
 * and only what the author actually wrote can be said to have survived or not.
 */
export async function checkSkinSurvival(
	descriptor: RawSkinDescriptor,
	options: SkinSurvivalOptions,
): Promise<SkinSurvivalReport> {
	const sources = options.sources ?? [
		{
			name: options.sourceName ?? DESCRIPTOR_SOURCE_NAME,
			load: async () => descriptor,
		} satisfies SkinSource,
	];
	// The resolver caches per host for a TTL; a caller checking several
	// descriptors in one process would otherwise read the first one's result.
	clearSkinCache();
	const skin = await resolveSkin({
		fallback: options.fallback,
		sources,
		host: null,
	});
	clearSkinCache();

	const mismatches: SkinLeafMismatch[] = [];
	collect(descriptor.appearance, skin.appearance, "appearance", mismatches);
	collect(descriptor.branding, skin.branding, "branding", mismatches);
	for (const key of Object.keys(descriptor)) {
		if (RESOLVED_DESCRIPTOR_KEYS.includes(key)) continue;
		// A top-level key beside `appearance`/`branding` is not validated — the
		// resolver simply never reads it — so it reaches no render and is reported
		// here rather than looking accepted.
		mismatches.push({
			path: key,
			declared: describe((descriptor as Record<string, unknown>)[key]),
			resolved: undefined,
		});
	}

	const sourceMatches =
		options.expectSource === undefined || skin.source === options.expectSource;
	return {
		skin,
		source: skin.source,
		mismatches,
		ok: mismatches.length === 0 && sourceMatches,
	};
}

/**
 * `checkSkinSurvival`, but throwing one error that names every lost leaf and the
 * winning source. This is the form a descriptor repository puts in its test
 * suite; it returns the resolved skin so the same call can assert values.
 */
export async function assertSkinSurvives(
	descriptor: RawSkinDescriptor,
	options: SkinSurvivalOptions,
): Promise<ResolvedSkin> {
	const report = await checkSkinSurvival(descriptor, options);
	if (report.ok) return report.skin;

	const lines: string[] = [];
	if (
		options.expectSource !== undefined &&
		report.source !== options.expectSource
	) {
		lines.push(
			`resolved from source '${report.source}', expected '${options.expectSource}'` +
				(report.source === "default"
					? " — the descriptor was rejected and the compiled default rendered instead"
					: ""),
		);
	}
	for (const mismatch of report.mismatches) {
		lines.push(
			`${mismatch.path}: declared ${mismatch.declared}, resolved ${describe(mismatch.resolved)}`,
		);
	}
	throw new Error(
		`skin descriptor did not survive resolution (${report.mismatches.length} field(s) lost):\n  ${lines.join("\n  ")}`,
	);
}

/**
 * Walk the declared side and compare each leaf against the same path on the
 * resolved side. Objects recurse; every other declared value is a leaf, including
 * one whose resolved counterpart is an object (a shape change loses the value
 * just as surely as a dropped key).
 */
function collect(
	declared: unknown,
	resolved: unknown,
	path: string,
	out: SkinLeafMismatch[],
): void {
	if (declared === undefined) return;
	if (isPlainObject(declared)) {
		if (!isPlainObject(resolved)) {
			out.push({ path, declared: describe(declared), resolved });
			return;
		}
		for (const [key, value] of Object.entries(declared)) {
			collect(value, resolved[key], `${path}.${key}`, out);
		}
		return;
	}
	if (resolved !== declared)
		out.push({ path, declared: describe(declared), resolved });
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function describe(value: unknown): string {
	if (typeof value === "string") return JSON.stringify(value);
	if (value === undefined) return "nothing";
	if (isPlainObject(value)) return `{${Object.keys(value).join(", ")}}`;
	return String(value);
}
