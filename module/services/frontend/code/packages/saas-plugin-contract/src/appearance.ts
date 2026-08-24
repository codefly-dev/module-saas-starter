import {
	FRONTEND_APPEARANCE_TOKEN_NAMES,
	type FrontendAppearance,
	type FrontendAppearanceDefinition,
	type FrontendThemeTokens,
} from "./contracts.js";

const light: FrontendThemeTokens = {
	background: "oklch(1 0 0)",
	foreground: "oklch(0.145 0 0)",
	card: "oklch(1 0 0)",
	cardForeground: "oklch(0.145 0 0)",
	popover: "oklch(1 0 0)",
	popoverForeground: "oklch(0.145 0 0)",
	primary: "oklch(0.205 0 0)",
	primaryForeground: "oklch(0.985 0 0)",
	secondary: "oklch(0.97 0 0)",
	secondaryForeground: "oklch(0.205 0 0)",
	muted: "oklch(0.97 0 0)",
	mutedForeground: "oklch(0.556 0 0)",
	accent: "oklch(0.97 0 0)",
	accentForeground: "oklch(0.205 0 0)",
	destructive: "oklch(0.577 0.245 27.325)",
	border: "oklch(0.922 0 0)",
	input: "oklch(0.922 0 0)",
	ring: "oklch(0.708 0 0)",
	sidebar: "oklch(0.985 0 0)",
	sidebarForeground: "oklch(0.145 0 0)",
	sidebarPrimary: "oklch(0.205 0 0)",
	sidebarPrimaryForeground: "oklch(0.985 0 0)",
	sidebarAccent: "oklch(0.97 0 0)",
	sidebarAccentForeground: "oklch(0.205 0 0)",
	sidebarBorder: "oklch(0.922 0 0)",
	sidebarRing: "oklch(0.708 0 0)",
	chart1: "oklch(0.87 0 0)",
	chart2: "oklch(0.556 0 0)",
	chart3: "oklch(0.439 0 0)",
	chart4: "oklch(0.371 0 0)",
	chart5: "oklch(0.269 0 0)",
};

const dark: FrontendThemeTokens = {
	background: "oklch(0.145 0 0)",
	foreground: "oklch(0.985 0 0)",
	card: "oklch(0.205 0 0)",
	cardForeground: "oklch(0.985 0 0)",
	popover: "oklch(0.205 0 0)",
	popoverForeground: "oklch(0.985 0 0)",
	primary: "oklch(0.922 0 0)",
	primaryForeground: "oklch(0.205 0 0)",
	secondary: "oklch(0.269 0 0)",
	secondaryForeground: "oklch(0.985 0 0)",
	muted: "oklch(0.269 0 0)",
	mutedForeground: "oklch(0.708 0 0)",
	accent: "oklch(0.269 0 0)",
	accentForeground: "oklch(0.985 0 0)",
	destructive: "oklch(0.704 0.191 22.216)",
	border: "oklch(1 0 0 / 10%)",
	input: "oklch(1 0 0 / 15%)",
	ring: "oklch(0.556 0 0)",
	sidebar: "oklch(0.205 0 0)",
	sidebarForeground: "oklch(0.985 0 0)",
	sidebarPrimary: "oklch(0.488 0.243 264.376)",
	sidebarPrimaryForeground: "oklch(0.985 0 0)",
	sidebarAccent: "oklch(0.269 0 0)",
	sidebarAccentForeground: "oklch(0.985 0 0)",
	sidebarBorder: "oklch(1 0 0 / 10%)",
	sidebarRing: "oklch(0.556 0 0)",
	chart1: "oklch(0.87 0 0)",
	chart2: "oklch(0.556 0 0)",
	chart3: "oklch(0.439 0 0)",
	chart4: "oklch(0.371 0 0)",
	chart5: "oklch(0.269 0 0)",
};

export const DEFAULT_FRONTEND_APPEARANCE: FrontendAppearance = deepFreeze({
	defaultTheme: "system",
	radius: "0.625rem",
	// Preserve the original SaaS Starter typography as the application
	// default. Applications may opt into a bundled/web font explicitly, but
	// the reusable Starter must not depend on an undeclared CSS variable.
	fontSans:
		'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
	fontHeading:
		'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
	// Structural defaults reproduce today's exact layout; omitting any of them
	// leaves the rendered product byte-for-byte unchanged.
	spacing: "0.25rem",
	fontSizeBase: "1rem",
	sidebarWidth: "16rem",
	sidebarWidthIcon: "3rem",
	borderWidth: "1px",
	shadowStrength: "1",
	light,
	dark,
});

const SAFE_CSS_VALUE = /^[^{};\r\n]+$/;
const SAFE_LENGTH = /^(?:0|(?:\d+(?:\.\d+)?)(?:px|rem|em))$/;
const SAFE_RADIUS = SAFE_LENGTH;

function assertAppearance(
	condition: unknown,
	message: string,
): asserts condition {
	if (!condition)
		throw new Error(`Invalid frontend plugin composition: ${message}`);
}

function exactKeys(value: object, allowed: readonly string[], context: string) {
	const unknown = Object.keys(value).filter((key) => !allowed.includes(key));
	assertAppearance(
		unknown.length === 0,
		`${context} has unknown field '${unknown[0]}'`,
	);
}

function validateValue(
	value: unknown,
	context: string,
): asserts value is string {
	assertAppearance(
		typeof value === "string" &&
			value.trim().length > 0 &&
			value.length <= 256 &&
			SAFE_CSS_VALUE.test(value),
		`${context} must be a safe non-empty CSS value`,
	);
}

function resolveTokens(
	mode: "light" | "dark",
	overrides: FrontendAppearanceDefinition["light"],
): FrontendThemeTokens {
	if (overrides === undefined) return DEFAULT_FRONTEND_APPEARANCE[mode];
	assertAppearance(
		overrides !== null &&
			typeof overrides === "object" &&
			!Array.isArray(overrides),
		`${mode} appearance tokens must be an object`,
	);
	exactKeys(overrides, FRONTEND_APPEARANCE_TOKEN_NAMES, `${mode} appearance`);
	for (const [name, value] of Object.entries(overrides))
		validateValue(value, `${mode} appearance token '${name}'`);
	return Object.freeze({ ...DEFAULT_FRONTEND_APPEARANCE[mode], ...overrides });
}

export function resolveFrontendAppearance(
	definition: FrontendAppearanceDefinition | undefined,
): FrontendAppearance {
	if (definition === undefined) return DEFAULT_FRONTEND_APPEARANCE;
	assertAppearance(
		definition !== null &&
			typeof definition === "object" &&
			!Array.isArray(definition),
		"appearance must be an object",
	);
	exactKeys(
		definition,
		[
			"defaultTheme",
			"radius",
			"fontSans",
			"fontHeading",
			"spacing",
			"fontSizeBase",
			"sidebarWidth",
			"sidebarWidthIcon",
			"borderWidth",
			"shadowStrength",
			"light",
			"dark",
		],
		"appearance",
	);
	const defaultTheme =
		definition.defaultTheme ?? DEFAULT_FRONTEND_APPEARANCE.defaultTheme;
	assertAppearance(
		defaultTheme === "light" ||
			defaultTheme === "dark" ||
			defaultTheme === "system",
		`appearance defaultTheme '${String(defaultTheme)}' is unsupported`,
	);
	const radius = definition.radius ?? DEFAULT_FRONTEND_APPEARANCE.radius;
	assertAppearance(
		typeof radius === "string" && SAFE_RADIUS.test(radius),
		"appearance radius must be 0 or a px/rem/em length",
	);
	const fontSans = definition.fontSans ?? DEFAULT_FRONTEND_APPEARANCE.fontSans;
	const fontHeading = definition.fontHeading ?? fontSans;
	validateValue(fontSans, "appearance fontSans");
	validateValue(fontHeading, "appearance fontHeading");
	const spacing = resolveLength("spacing", definition.spacing);
	const fontSizeBase = resolveLength("fontSizeBase", definition.fontSizeBase);
	const sidebarWidth = resolveLength("sidebarWidth", definition.sidebarWidth);
	const sidebarWidthIcon = resolveLength(
		"sidebarWidthIcon",
		definition.sidebarWidthIcon,
	);
	const borderWidth = resolveLength("borderWidth", definition.borderWidth);
	const shadowStrength = resolveShadowStrength(definition.shadowStrength);
	return deepFreeze({
		defaultTheme,
		radius,
		fontSans,
		fontHeading,
		spacing,
		fontSizeBase,
		sidebarWidth,
		sidebarWidthIcon,
		borderWidth,
		shadowStrength,
		light: resolveTokens("light", definition.light),
		dark: resolveTokens("dark", definition.dark),
	});
}

function resolveLength(
	field: keyof FrontendAppearance,
	value: string | undefined,
): string {
	const resolved = value ?? (DEFAULT_FRONTEND_APPEARANCE[field] as string);
	assertAppearance(
		typeof resolved === "string" && SAFE_LENGTH.test(resolved),
		`appearance ${field} must be 0 or a px/rem/em length`,
	);
	return resolved;
}

function resolveShadowStrength(value: string | undefined): string {
	const resolved = value ?? DEFAULT_FRONTEND_APPEARANCE.shadowStrength;
	const parsed = Number(resolved);
	assertAppearance(
		typeof resolved === "string" &&
			/^\d+(?:\.\d+)?$/.test(resolved) &&
			Number.isFinite(parsed) &&
			parsed >= 0 &&
			parsed <= 2,
		"appearance shadowStrength must be a unitless number between 0 and 2",
	);
	return resolved;
}

function deepFreeze<T extends FrontendAppearance>(appearance: T): T {
	Object.freeze(appearance.light);
	Object.freeze(appearance.dark);
	return Object.freeze(appearance);
}
