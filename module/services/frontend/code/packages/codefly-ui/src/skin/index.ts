export {
	CACHE_MAX_ENTRIES,
	clearSkinCache,
	type ResolveSkinOptions,
	resolveSkin,
} from "./resolver.js";
export {
	assertSkinSurvives,
	checkSkinSurvival,
	type SkinLeafMismatch,
	type SkinSurvivalOptions,
	type SkinSurvivalReport,
} from "./survival.js";
export type {
	RawBrandingOverride,
	RawSkinDescriptor,
	ResolvedSkin,
	ResolvedSkinBase,
	SkinKey,
	SkinSource,
} from "./types.js";
