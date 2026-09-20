import type {
	FrontendAppearance,
	FrontendAppearanceDefinition,
	FrontendBranding,
	FrontendLogo,
	FrontendSkinRules,
} from "@codefly/saas-plugin-contract";

/**
 * The application skin resolved for a request: validated appearance + branding
 * ready to render. This is the shape both the compiled default and any runtime
 * source ultimately produce.
 */
export interface ResolvedSkinBase {
	appearance: FrontendAppearance;
	branding: FrontendBranding;
}

export interface ResolvedSkin extends ResolvedSkinBase {
	/** Which source produced the skin; "default" when nothing overrode it. */
	source: string;
	/**
	 * Layer 4. Build-time constraints, never rendered: `checkSkinRules` reads
	 * them against a rendered tree and nothing here reaches CSS.
	 */
	rules: FrontendSkinRules;
}

/**
 * Untrusted skin descriptor as delivered by a runtime source (env blob,
 * mounted ConfigMap file, or a CMS/config API). Every field is validated
 * before use — the appearance through the contract's `resolveFrontendAppearance`
 * and branding assets through an HTTPS/relative allowlist. Unknown or unsafe
 * values fall back to the compiled default, so a bad descriptor can never break
 * a page or inject CSS.
 */
export interface RawSkinDescriptor {
	appearance?: FrontendAppearanceDefinition;
	branding?: RawBrandingOverride;
	/**
	 * Layer 4: constraints checked at build time, never rendered.
	 */
	rules?: unknown;
}

// `remotes` was declared here as RESERVED for the vetted micro-frontend tier and
// never resolved. A descriptor field nothing reads is worse than an absent one:
// it reads as a promise, and a descriptor carrying it validated cleanly while
// the value went nowhere. The ladder's third rung is still written down — in
// TOKENS.md, where it is documentation — and `checkSkinSurvival` now reports a
// top-level key the resolver does not read, so an author who writes one is told.

export interface RawBrandingOverride {
	name?: string;
	mark?: string;
	title?: string;
	description?: string;
	favicon?: string;
	logo?: FrontendLogo;
}

/** What a skin is keyed on. Host/domain today; room for org/env later. */
export interface SkinKey {
	host: string | null;
}

/**
 * A pluggable place skins come from. The env, mounted-file, and HTTP/CMS
 * adapters all implement this one seam, so the delivery mechanism can be
 * compared or swapped without touching the resolver or the render path.
 */
export interface SkinSource {
	readonly name: string;
	load(key: SkinKey): Promise<RawSkinDescriptor | null>;
}
