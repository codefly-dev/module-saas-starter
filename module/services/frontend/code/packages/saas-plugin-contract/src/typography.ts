// Layers 1 to 3 of the skin vocabulary: a type SCALE, the ROLES that point into
// it, the SLOTS that say which role a surface uses, and the control rungs that
// are the same pattern for geometry.
//
// Why the indirection. Colour already reskins because every layer names a token
// (`bg-primary`) rather than a value. Type, weight and control geometry never got
// that treatment: they are raw utilities compiled into component source, and in
// one class list a primitive and a semantic utility are indistinguishable —
// `bg-primary text-primary-foreground` reskins, `text-sm font-medium h-8` is
// welded in. Collapsing four different things into one namespace is what makes a
// token set look unable to carry a real design system.
//
//   SCALE   ordinal values, no meaning:      step 3 is 0.875rem
//   ROLE    a named bundle pointing at one:  `menu-item` is step 3 at 1.25rem
//   SLOT    which role a surface uses:       `command-item` uses `menu-item`
//   RUNG    the same, for control geometry:  `sm` is 7 spacing units tall
//
// A role is a pointer INTO the scale, so roles → scale is total and lossless and
// the reverse is never needed. A customer names `Display 01`; the ramp stays
// ordinal underneath. Two design-system flavours become two role maps over one
// scale, which is exactly what a single `fontSizeBase` multiplier cannot express.
//
// Every field of a role is OPTIONAL, and that is deliberate rather than lax: a
// role declares only the properties it actually decides. `emphasis` is a weight
// and nothing else, because `table-head` sets `font-medium` today and inherits
// its size. Forcing a value for every property would emit a declaration where
// the tree currently inherits one, and the defaults here have to reproduce what
// the kit renders today to the pixel.

export const FRONTEND_TYPE_SCALE_STEPS = [
	"1",
	"2",
	"3",
	"4",
	"5",
	"6",
	"7",
	"8",
	"9",
] as const;
export type FrontendTypeScaleStep = (typeof FRONTEND_TYPE_SCALE_STEPS)[number];
export type FrontendTypeScale = Readonly<Record<FrontendTypeScaleStep, string>>;

export const FRONTEND_TYPE_ROLE_NAMES = [
	"surface-title",
	"surface-title-snug",
	"surface-title-tight",
	"surface-title-compact",
	"surface-title-plain",
	"surface-description",
	"plain",
	"page-title",
	"section-title",
	"body",
	"emphasis",
	"control-label",
	"menu-item",
	"group-label",
	"group-label-plain",
	"shortcut",
	"control",
	"control-sm",
	"control-xs",
	"control-touch",
	"metric-value",
	"metric-value-lg",
	"metric-total",
	"metric-unit",
	"caption",
	"chart-label",
] as const;
export type FrontendTypeRoleName = (typeof FRONTEND_TYPE_ROLE_NAMES)[number];

export const FRONTEND_TYPE_WEIGHTS = ["400", "500", "600", "700"] as const;
export type FrontendTypeWeight = (typeof FRONTEND_TYPE_WEIGHTS)[number];
export const FRONTEND_TYPE_FAMILIES = ["sans", "heading", "mono"] as const;
export type FrontendTypeFamily = (typeof FRONTEND_TYPE_FAMILIES)[number];

/**
 * One role. Size, weight, line height and tracking travel together here because
 * three parallel scales cannot express two systems that use the same sizes at
 * different weights — the reason a global multiplier is not enough.
 */
export interface FrontendTypeRole {
	size?: FrontendTypeScaleStep;
	weight?: FrontendTypeWeight;
	/** Unitless ratio (`1.375`) or a length (`1.25rem`). */
	lineHeight?: string;
	/** Letter spacing: `normal`, `0`, or an em/px length. */
	tracking?: FrontendTypeTracking;
	family?: FrontendTypeFamily;
}
export type FrontendTypeTracking = string;
export type FrontendTypeRoles = Readonly<
	Record<FrontendTypeRoleName, Readonly<FrontendTypeRole>>
>;

/**
 * The surfaces a role can be attached to. These are the kit's own `data-slot`
 * names, not an invented vocabulary: the kit already carries 142 of them across
 * its components, so layer 3 is wiring a naming convention that is present and
 * stable rather than new construction. A few name a component STATE rather than
 * an element (`card-title-sm` is the title inside a small card), because that is
 * where a distinct decision genuinely lives.
 */
export const FRONTEND_TYPE_SLOT_NAMES = [
	// Generic surfaces. Not every type decision belongs to a named component:
	// plain body copy, a caption and an emphasised run are decisions in their own
	// right, and naming them keeps a component from reaching for a raw utility
	// just because its surface has no component-specific slot.
	"body",
	"caption-plain",
	"emphasis",
	"card",
	"card-heading",
	"card-title",
	"card-title-sm",
	"card-description",
	"dialog-content",
	"dialog-title",
	"dialog-description",
	"sheet-content",
	"sheet-title",
	"sheet-description",
	"alert-dialog-title",
	"alert-dialog-description",
	"page-title",
	"section-title",
	"section-description",
	"label",
	"input",
	"input-touch",
	"textarea",
	"textarea-touch",
	"select-trigger",
	"select-label",
	"select-item",
	"dropdown-menu-item",
	"dropdown-menu-checkbox-item",
	"dropdown-menu-radio-item",
	"dropdown-menu-sub-trigger",
	"dropdown-menu-label",
	"dropdown-menu-shortcut",
	"command-input",
	"command-group",
	"command-item",
	"command-empty",
	"command-shortcut",
	"badge",
	"table",
	"segmented-control-segment",
	"pagination-ellipsis",
	"field-description",
	"field-error",
	"table-toolbar",
	"table-empty-state",
	"card-eyebrow",
	"card-metadata",
	"table-head",
	"table-footer",
	"table-caption",
	"tabs-trigger",
	"tabs-content",
	"tooltip-content",
	"avatar-fallback",
	"avatar-fallback-sm",
	"avatar-group-count",
	"sidebar-group-content",
	"sidebar-group-label",
	"sidebar-menu-button",
	"sidebar-menu-button-sm",
	"sidebar-menu-button-lg",
	"sidebar-menu-button-active",
	"sidebar-menu-sub-button",
	"sidebar-menu-sub-button-sm",
	"sidebar-menu-badge",
	"error-state",
	"error-state-title",
	"input-group-addon",
	"input-group-text",
	"input-group-control",
	"empty-state-title",
	"empty-state-title-illustrated",
	"empty-state-description",
	"metric-value",
	"metric-value-lg",
	"metric-unit",
	"metric-total",
	"metric-delta",
	"metric-delta-label",
	"metric-label",
	"metric-heading",
	"chart-label",
	"chat-author",
	"chat-message",
] as const;
export type FrontendTypeSlotName = (typeof FRONTEND_TYPE_SLOT_NAMES)[number];
export type FrontendTypeSlots = Readonly<
	Record<FrontendTypeSlotName, FrontendTypeRoleName>
>;

export const FRONTEND_CONTROL_SIZE_NAMES = [
	"xs",
	"sm",
	"default",
	"lg",
] as const;
export type FrontendControlSizeName =
	(typeof FRONTEND_CONTROL_SIZE_NAMES)[number];

/**
 * A control rung. Height, horizontal padding and icon size are **spacing
 * multiples**, not lengths: today they ride Tailwind's `--spacing` unit (`h-8` is
 * `calc(var(--spacing) * 8)`), so storing lengths would silently unhook control
 * geometry from a skin's density setting.
 *
 * The rung CARRIES its text role. That answers the open question about whether
 * type belongs to a size variant or to a slot: for a control it belongs to the
 * rung, because a button's type is decided by how big the button is, and a caller
 * choosing `size="sm"` is choosing the whole rung.
 */
export interface FrontendControlSize {
	/** Outer height, in `--spacing` units. */
	height: string;
	/** Horizontal padding, in `--spacing` units. */
	paddingX: string;
	/** Icon edge, in `--spacing` units. */
	icon: string;
	/** The type role this rung's label uses. */
	text: FrontendTypeRoleName;
}
export type FrontendControlSizes = Readonly<
	Record<FrontendControlSizeName, Readonly<FrontendControlSize>>
>;

export type FrontendTypeScaleOverrides = Readonly<
	Partial<Record<FrontendTypeScaleStep, string>>
>;
export type FrontendTypeRoleOverrides = Readonly<
	Partial<Record<FrontendTypeRoleName, FrontendTypeRole>>
>;
export type FrontendTypeSlotOverrides = Readonly<
	Partial<Record<FrontendTypeSlotName, FrontendTypeRoleName>>
>;
export type FrontendControlSizeOverrides = Readonly<
	Partial<Record<FrontendControlSizeName, Partial<FrontendControlSize>>>
>;

/**
 * Layer 1. Ordinal, meaningless values. Steps 2 to 9 reproduce the sizes the kit
 * uses today; step 1 exists because the dashboard escapes the scale entirely with
 * bracketed 10px and 11px literals, which cannot be reskinned or audited.
 */
export const DEFAULT_TYPE_SCALE: FrontendTypeScale = Object.freeze({
	"1": "0.625rem",
	"2": "0.75rem",
	"3": "0.875rem",
	"4": "1rem",
	"5": "1.125rem",
	"6": "1.25rem",
	"7": "1.5rem",
	"8": "1.875rem",
	"9": "2.25rem",
});

/**
 * Layer 2. Each role reproduces exactly what the kit renders at the slots that
 * use it today, so adopting the vocabulary moves nothing on screen.
 *
 * Three title roles differ only in line height (`1.5rem` / `1.375` / `1`) because
 * the sheet, card and dialog titles differ only in line height today. They are
 * kept apart rather than merged so this change moves no pixel; merging them is a
 * design decision for whoever owns the system, recorded on the issue.
 */
export const DEFAULT_TYPE_ROLES: FrontendTypeRoles = Object.freeze({
	"surface-title": Object.freeze({
		size: "4",
		weight: "500",
		lineHeight: "1.5rem",
		family: "heading",
	}),
	"surface-title-snug": Object.freeze({
		size: "4",
		weight: "500",
		lineHeight: "1.375",
		family: "heading",
	}),
	"surface-title-tight": Object.freeze({
		size: "4",
		weight: "500",
		lineHeight: "1",
		family: "heading",
	}),
	"surface-title-compact": Object.freeze({
		size: "3",
		weight: "500",
		lineHeight: "1.375",
		family: "heading",
	}),
	// The simple Card's heading takes the size and weight of a surface title but
	// neither the heading family nor a line height of its own.
	"surface-title-plain": Object.freeze({ size: "4", weight: "500" }),
	"surface-description": Object.freeze({ size: "3", lineHeight: "1.25rem" }),
	// Weight alone, stepping back OUT of an inherited one: a delta's label sits
	// inside an emphasised run and must not inherit its weight.
	plain: Object.freeze({ weight: "400" }),
	"page-title": Object.freeze({
		size: "7",
		weight: "700",
		lineHeight: "2rem",
		tracking: "-0.025em",
	}),
	"section-title": Object.freeze({
		size: "5",
		weight: "600",
		lineHeight: "1.75rem",
		tracking: "-0.025em",
	}),
	body: Object.freeze({ size: "3", lineHeight: "1.25rem" }),
	// Weight alone: `table-head` and `table-footer` set `font-medium` and inherit
	// their size from the table. A role that forced a size would change them.
	emphasis: Object.freeze({ weight: "500" }),
	"control-label": Object.freeze({
		size: "3",
		weight: "500",
		lineHeight: "1",
	}),
	"menu-item": Object.freeze({ size: "3", lineHeight: "1.25rem" }),
	"group-label": Object.freeze({
		size: "2",
		weight: "500",
		lineHeight: "1rem",
	}),
	"group-label-plain": Object.freeze({ size: "2", lineHeight: "1rem" }),
	shortcut: Object.freeze({
		size: "2",
		lineHeight: "1rem",
		tracking: "0.1em",
	}),
	control: Object.freeze({ size: "3", weight: "500", lineHeight: "1.25rem" }),
	// The one deliberate change in this vocabulary: the small button renders
	// `text-[0.8rem]` (12.8px) today, a magic number off any scale. It lands on
	// step 2 (12px). Recorded rather than hidden.
	"control-sm": Object.freeze({
		size: "2",
		weight: "500",
		lineHeight: "1rem",
	}),
	"control-xs": Object.freeze({
		size: "2",
		weight: "500",
		lineHeight: "1rem",
	}),
	// The size a text input takes on a touch viewport, where a sub-16px field makes
	// mobile browsers zoom on focus. The `md`-and-up size is the `input` slot.
	"control-touch": Object.freeze({ size: "4", lineHeight: "1.5rem" }),
	"metric-value": Object.freeze({
		size: "7",
		weight: "600",
		lineHeight: "2rem",
		tracking: "-0.025em",
	}),
	"metric-value-lg": Object.freeze({
		size: "8",
		weight: "600",
		lineHeight: "2.25rem",
		tracking: "-0.025em",
	}),
	// A metric's unit sits inside its value and must step back OUT of the value's
	// weight, so this role declares 400 rather than leaving weight to inherit.
	"metric-unit": Object.freeze({
		size: "3",
		weight: "400",
		lineHeight: "1.25rem",
	}),
	"metric-total": Object.freeze({
		size: "9",
		weight: "700",
		lineHeight: "2.5rem",
		tracking: "-0.025em",
	}),
	caption: Object.freeze({ size: "2", weight: "500", lineHeight: "1rem" }),
	// The dashboard's 10px and 11px bracketed literals both land here. 11px → 10px
	// is the second deliberate change; a magic number cannot be reskinned.
	"chart-label": Object.freeze({ size: "1", lineHeight: "1" }),
});

/**
 * Layer 3. Which role each surface uses — the layer that stops a component from
 * encoding a design decision. `CardTitle` hardcodes `text-base font-medium`
 * today, so honouring a customer who wants a different role there means editing
 * kit source; with a slot map the component declares WHERE a decision applies and
 * the skin decides WHICH decision that is.
 */
export const DEFAULT_TYPE_SLOTS: FrontendTypeSlots = Object.freeze({
	body: "body",
	"caption-plain": "group-label-plain",
	emphasis: "emphasis",
	card: "body",
	"card-heading": "surface-title-plain",
	"card-title": "surface-title-snug",
	"card-title-sm": "surface-title-compact",
	"card-description": "surface-description",
	"dialog-content": "body",
	"dialog-title": "surface-title-tight",
	"dialog-description": "surface-description",
	"sheet-content": "body",
	"sheet-title": "surface-title",
	"sheet-description": "surface-description",
	"alert-dialog-title": "surface-title",
	"alert-dialog-description": "surface-description",
	"page-title": "page-title",
	"section-title": "section-title",
	"section-description": "body",
	label: "control-label",
	// A text input renders at step 4 on a touch viewport so mobile browsers do not
	// zoom on focus, and at step 3 from `md` up. Two slots rather than a
	// breakpoint inside a role: both are real, separately re-pointable decisions,
	// and the role stays a pure bundle with no viewport in it.
	input: "body",
	"input-touch": "control-touch",
	textarea: "body",
	"textarea-touch": "control-touch",
	"select-trigger": "body",
	"select-label": "group-label-plain",
	"select-item": "menu-item",
	"dropdown-menu-item": "menu-item",
	"dropdown-menu-checkbox-item": "menu-item",
	"dropdown-menu-radio-item": "menu-item",
	"dropdown-menu-sub-trigger": "menu-item",
	"dropdown-menu-label": "group-label",
	"dropdown-menu-shortcut": "shortcut",
	"command-input": "body",
	"command-group": "group-label",
	"command-item": "menu-item",
	"command-empty": "body",
	"command-shortcut": "shortcut",
	badge: "group-label",
	table: "body",
	"segmented-control-segment": "control",
	"pagination-ellipsis": "body",
	"field-description": "surface-description",
	"field-error": "surface-description",
	"table-toolbar": "body",
	"table-empty-state": "body",
	"card-eyebrow": "group-label",
	"card-metadata": "body",
	"table-head": "emphasis",
	"table-footer": "emphasis",
	"table-caption": "body",
	"tabs-trigger": "control",
	"tabs-content": "body",
	"tooltip-content": "group-label-plain",
	"avatar-fallback": "body",
	"avatar-fallback-sm": "group-label-plain",
	"avatar-group-count": "body",
	"sidebar-group-content": "body",
	"sidebar-group-label": "group-label",
	"sidebar-menu-button": "body",
	"sidebar-menu-button-sm": "group-label-plain",
	"sidebar-menu-button-lg": "body",
	"sidebar-menu-button-active": "emphasis",
	"sidebar-menu-sub-button": "body",
	"sidebar-menu-sub-button-sm": "group-label-plain",
	"sidebar-menu-badge": "group-label",
	"error-state": "body",
	"error-state-title": "emphasis",
	"input-group-addon": "control",
	"input-group-text": "body",
	"input-group-control": "body",
	"empty-state-title": "control",
	"empty-state-title-illustrated": "section-title",
	"empty-state-description": "body",
	"metric-value": "metric-value",
	"metric-value-lg": "metric-value-lg",
	"metric-unit": "metric-unit",
	"metric-total": "metric-total",
	"metric-delta": "caption",
	"metric-delta-label": "plain",
	"metric-label": "body",
	"metric-heading": "control",
	"chart-label": "chart-label",
	"chat-author": "caption",
	"chat-message": "body",
});

/** Geometry rungs, reproducing the kit's current button and input geometry. */
export const DEFAULT_CONTROL_SIZES: FrontendControlSizes = Object.freeze({
	xs: Object.freeze({
		height: "6",
		paddingX: "2",
		icon: "3",
		text: "control-xs" as const,
	}),
	sm: Object.freeze({
		height: "7",
		paddingX: "2.5",
		icon: "3.5",
		text: "control-sm" as const,
	}),
	default: Object.freeze({
		height: "8",
		paddingX: "2.5",
		icon: "4",
		text: "control" as const,
	}),
	lg: Object.freeze({
		height: "9",
		paddingX: "2.5",
		icon: "4",
		text: "control" as const,
	}),
});

/** A flattened slot: what a renderer actually needs to paint one surface. */
export interface ResolvedTypeSlot {
	role: FrontendTypeRoleName;
	fontSize?: string;
	fontWeight?: FrontendTypeWeight;
	lineHeight?: string;
	letterSpacing?: FrontendTypeTracking;
	fontFamily?: FrontendTypeFamily;
}

export interface TypographyLayers {
	typeScale: FrontendTypeScale;
	typeRoles: FrontendTypeRoles;
	typeSlots: FrontendTypeSlots;
	controlSizes: FrontendControlSizes;
}

/**
 * Flatten one slot through its role into the scale. The host, the kit and a skin
 * author all call this rather than each walking the layers, so they cannot
 * disagree about what a slot renders as.
 */
export function resolveTypeSlot(
	layers: TypographyLayers,
	slot: FrontendTypeSlotName,
): ResolvedTypeSlot {
	const role = layers.typeSlots[slot];
	return resolveTypeRole(layers, role);
}

/** Flatten a role. Separate from `resolveTypeSlot` because a control rung names a role directly. */
export function resolveTypeRole(
	layers: Pick<TypographyLayers, "typeScale" | "typeRoles">,
	role: FrontendTypeRoleName,
): ResolvedTypeSlot {
	const definition = layers.typeRoles[role];
	return {
		role,
		fontSize:
			definition.size === undefined
				? undefined
				: layers.typeScale[definition.size],
		fontWeight: definition.weight,
		lineHeight: definition.lineHeight,
		letterSpacing: definition.tracking,
		fontFamily: definition.family,
	};
}

// ---------------------------------------------------------------------------
// Validation. Fail-closed, exactly like the colour tokens: an unknown step,
// role, slot or rung throws rather than being ignored, so a descriptor written
// against a different vocabulary is refused loudly instead of half-applied.

function assertTypography(
	condition: unknown,
	message: string,
): asserts condition {
	if (!condition)
		throw new Error(`Invalid frontend plugin composition: ${message}`);
}

const SAFE_TYPE_LENGTH = /^(?:0|(?:\d+(?:\.\d+)?)(?:px|rem|em))$/;
/** Unitless ratio (`1.375`) or a length. Both are legal CSS line heights. */
const SAFE_LINE_HEIGHT = /^(?:\d+(?:\.\d+)?|(?:\d+(?:\.\d+)?)(?:px|rem|em))$/;
/** `normal`, `0`, or a signed em/px length — tracking is routinely negative. */
const SAFE_TRACKING = /^(?:normal|0|-?\d+(?:\.\d+)?(?:em|px))$/;
/** A `--spacing` multiple: a positive decimal, never a length. */
const SAFE_SPACING_MULTIPLE = /^\d+(?:\.\d+)?$/;

function assertObject(
	value: unknown,
	context: string,
): asserts value is Record<string, unknown> {
	assertTypography(
		value !== null && typeof value === "object" && !Array.isArray(value),
		`${context} must be an object`,
	);
}

function assertKnownKeys(
	value: Record<string, unknown>,
	allowed: readonly string[],
	context: string,
): void {
	const unknown = Object.keys(value).filter((key) => !allowed.includes(key));
	assertTypography(
		unknown.length === 0,
		`${context} has unknown field '${unknown[0]}'`,
	);
}

function resolveTypeScale(
	overrides: FrontendTypeScaleOverrides | undefined,
): FrontendTypeScale {
	if (overrides === undefined) return DEFAULT_TYPE_SCALE;
	assertObject(overrides, "appearance typeScale");
	assertKnownKeys(overrides, FRONTEND_TYPE_SCALE_STEPS, "appearance typeScale");
	for (const [step, value] of Object.entries(overrides)) {
		assertTypography(
			typeof value === "string" && SAFE_TYPE_LENGTH.test(value),
			`appearance typeScale step '${step}' must be 0 or a px/rem/em length`,
		);
	}
	return Object.freeze({ ...DEFAULT_TYPE_SCALE, ...overrides });
}

const TYPE_ROLE_FIELDS = [
	"size",
	"weight",
	"lineHeight",
	"tracking",
	"family",
] as const;

function resolveTypeRoles(
	overrides: FrontendTypeRoleOverrides | undefined,
	scale: FrontendTypeScale,
): FrontendTypeRoles {
	if (overrides === undefined) return DEFAULT_TYPE_ROLES;
	assertObject(overrides, "appearance typeRoles");
	assertKnownKeys(overrides, FRONTEND_TYPE_ROLE_NAMES, "appearance typeRoles");
	const resolved: Record<string, Readonly<FrontendTypeRole>> = {
		...DEFAULT_TYPE_ROLES,
	};
	for (const [name, override] of Object.entries(overrides)) {
		const context = `appearance typeRole '${name}'`;
		assertObject(override, context);
		assertKnownKeys(override, TYPE_ROLE_FIELDS, context);
		// Field-wise merge: a skin restating one property of a role keeps the
		// rest, the same partiality the colour tokens have.
		const merged: FrontendTypeRole = {
			...DEFAULT_TYPE_ROLES[name as FrontendTypeRoleName],
			...(override as FrontendTypeRole),
		};
		if (merged.size !== undefined) {
			assertTypography(
				(FRONTEND_TYPE_SCALE_STEPS as readonly string[]).includes(merged.size),
				`${context} size '${String(merged.size)}' is not a scale step`,
			);
			assertTypography(
				scale[merged.size] !== undefined,
				`${context} size '${merged.size}' has no value in the scale`,
			);
		}
		if (merged.weight !== undefined)
			assertTypography(
				(FRONTEND_TYPE_WEIGHTS as readonly string[]).includes(merged.weight),
				`${context} weight '${String(merged.weight)}' is unsupported`,
			);
		if (merged.lineHeight !== undefined)
			assertTypography(
				typeof merged.lineHeight === "string" &&
					SAFE_LINE_HEIGHT.test(merged.lineHeight),
				`${context} lineHeight must be a ratio or a px/rem/em length`,
			);
		if (merged.tracking !== undefined)
			assertTypography(
				typeof merged.tracking === "string" &&
					SAFE_TRACKING.test(merged.tracking),
				`${context} tracking must be 'normal', 0, or an em/px length`,
			);
		if (merged.family !== undefined)
			assertTypography(
				(FRONTEND_TYPE_FAMILIES as readonly string[]).includes(merged.family),
				`${context} family '${String(merged.family)}' is unsupported`,
			);
		resolved[name] = Object.freeze(merged);
	}
	return Object.freeze(resolved) as FrontendTypeRoles;
}

function resolveTypeSlots(
	overrides: FrontendTypeSlotOverrides | undefined,
	roles: FrontendTypeRoles,
): FrontendTypeSlots {
	if (overrides === undefined) return DEFAULT_TYPE_SLOTS;
	assertObject(overrides, "appearance typeSlots");
	assertKnownKeys(overrides, FRONTEND_TYPE_SLOT_NAMES, "appearance typeSlots");
	for (const [slot, role] of Object.entries(overrides)) {
		assertTypography(
			typeof role === "string" &&
				(FRONTEND_TYPE_ROLE_NAMES as readonly string[]).includes(role),
			`appearance typeSlot '${slot}' names unknown role '${String(role)}'`,
		);
		assertTypography(
			roles[role as FrontendTypeRoleName] !== undefined,
			`appearance typeSlot '${slot}' names role '${role}' which is not declared`,
		);
	}
	return Object.freeze({ ...DEFAULT_TYPE_SLOTS, ...overrides });
}

const CONTROL_SIZE_FIELDS = ["height", "paddingX", "icon", "text"] as const;

function resolveControlSizes(
	overrides: FrontendControlSizeOverrides | undefined,
	roles: FrontendTypeRoles,
): FrontendControlSizes {
	if (overrides === undefined) return DEFAULT_CONTROL_SIZES;
	assertObject(overrides, "appearance controlSizes");
	assertKnownKeys(
		overrides,
		FRONTEND_CONTROL_SIZE_NAMES,
		"appearance controlSizes",
	);
	const resolved: Record<string, Readonly<FrontendControlSize>> = {
		...DEFAULT_CONTROL_SIZES,
	};
	for (const [name, override] of Object.entries(overrides)) {
		const context = `appearance controlSize '${name}'`;
		assertObject(override, context);
		assertKnownKeys(override, CONTROL_SIZE_FIELDS, context);
		const merged: FrontendControlSize = {
			...DEFAULT_CONTROL_SIZES[name as FrontendControlSizeName],
			...(override as FrontendControlSize),
		};
		for (const field of ["height", "paddingX", "icon"] as const) {
			assertTypography(
				typeof merged[field] === "string" &&
					SAFE_SPACING_MULTIPLE.test(merged[field]),
				`${context} ${field} must be a positive number of spacing units, not a length`,
			);
		}
		assertTypography(
			(FRONTEND_TYPE_ROLE_NAMES as readonly string[]).includes(merged.text) &&
				roles[merged.text] !== undefined,
			`${context} text names unknown role '${String(merged.text)}'`,
		);
		resolved[name] = Object.freeze(merged);
	}
	return Object.freeze(resolved) as FrontendControlSizes;
}

/** Resolve all four typography/geometry layers from a descriptor's overrides. */
export function resolveTypographyLayers(definition: {
	typeScale?: FrontendTypeScaleOverrides;
	typeRoles?: FrontendTypeRoleOverrides;
	typeSlots?: FrontendTypeSlotOverrides;
	controlSizes?: FrontendControlSizeOverrides;
}): TypographyLayers {
	const typeScale = resolveTypeScale(definition.typeScale);
	const typeRoles = resolveTypeRoles(definition.typeRoles, typeScale);
	const typeSlots = resolveTypeSlots(definition.typeSlots, typeRoles);
	const controlSizes = resolveControlSizes(definition.controlSizes, typeRoles);
	return { typeScale, typeRoles, typeSlots, controlSizes };
}
