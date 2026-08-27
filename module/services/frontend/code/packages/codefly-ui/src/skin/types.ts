import type {
	FrontendAppearance,
	FrontendAppearanceDefinition,
	FrontendBranding,
	FrontendLogo,
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
	 * RESERVED for Layer 4 (component-level overrides via the vetted micro-FE
	 * tier, per ADR-0002). Not resolved yet — declared so the descriptor schema
	 * anticipates it and sources can start carrying it.
	 */
	remotes?: unknown;
}

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
