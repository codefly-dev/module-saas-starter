import {
	FRONTEND_APPEARANCE_TOKEN_NAMES,
	FRONTEND_CONTROL_SIZE_NAMES,
	FRONTEND_TYPE_SLOT_NAMES,
	type FrontendAppearance,
	type FrontendAppearanceTokenName,
	type ResolvedTypeSlot,
	resolveTypeRole,
	resolveTypeSlot,
} from "@codefly/saas-plugin-contract";
import type { CSSProperties } from "react";

/**
 * The custom properties a resolved skin puts on `<html>`: the appearance tokens,
 * plus the flattened type slots and control rungs the generated utilities read.
 * A property a slot's role does not decide is absent rather than empty, so every
 * key is optional.
 */
export type AppearanceStyleProperties = CSSProperties &
	Partial<
		Record<
			`--appearance-${string}` | `--type-${string}` | `--control-${string}`,
			string
		>
	>;

export function appearanceVariableName(
	mode: "light" | "dark",
	token: FrontendAppearanceTokenName,
): `--appearance-${"light" | "dark"}-${string}` {
	return `--appearance-${mode}-${token.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)}`;
}

/**
 * Produces the SSR-safe custom properties consumed by globals.css. Keeping
 * both palettes on <html> lets the client theme provider switch one class
 * without recomputing the tenant appearance tokens.
 */
export function appearanceStyleProperties(
	appearance: FrontendAppearance,
): AppearanceStyleProperties {
	const properties: Record<string, string> = {
		"--appearance-radius": appearance.radius,
		"--appearance-font-sans": appearance.fontSans,
		"--appearance-font-heading": appearance.fontHeading,
		"--appearance-font-mono": appearance.fontMono,
		"--appearance-spacing": appearance.spacing,
		"--appearance-font-size-base": appearance.fontSizeBase,
		"--appearance-sidebar-width": appearance.sidebarWidth,
		"--appearance-sidebar-width-icon": appearance.sidebarWidthIcon,
		"--appearance-border-width": appearance.borderWidth,
		"--appearance-shadow-strength": appearance.shadowStrength,
	};
	// Optional values are projected only when the skin decided them; the
	// stylesheet's `var(--…, fallback)` derives the rest. `--appearance-…-disabled
	// -opacity` is the presence signal for the disabled fill: a decided fill
	// renders flat at full opacity, an absent one keeps the faded variant.
	if (appearance.buttonRadius !== undefined)
		properties["--appearance-button-radius"] = appearance.buttonRadius;
	for (const mode of ["light", "dark"] as const) {
		for (const token of FRONTEND_APPEARANCE_TOKEN_NAMES) {
			const value = appearance[mode][token];
			if (value !== undefined)
				properties[appearanceVariableName(mode, token)] = value;
		}
		if (appearance[mode].disabled !== undefined)
			properties[`--appearance-${mode}-disabled-opacity`] = "1";
	}
	// Layers 1 to 3, flattened. The kit's generated stylesheet declares one
	// utility per slot reading exactly the variables the slot's role decides, and
	// this projects exactly those: both derive the set from the same contract,
	// which refuses a skin that would change it. A property the role does not
	// decide is therefore absent on both sides — never declared, never projected
	// — so the element keeps whatever else styles it, as it did before slots.
	for (const slot of FRONTEND_TYPE_SLOT_NAMES)
		writeTypeProperties(
			properties,
			`--type-${slot}`,
			resolveTypeSlot(appearance, slot),
		);
	for (const size of FRONTEND_CONTROL_SIZE_NAMES) {
		const rung = appearance.controlSizes[size];
		// Geometry is stored as `--spacing` multiples, not lengths, so a skin that
		// changes density moves control geometry with it instead of pinning it.
		properties[`--control-${size}-height`] = spacingUnits(rung.height);
		properties[`--control-${size}-padding-x`] = spacingUnits(rung.paddingX);
		properties[`--control-${size}-icon`] = spacingUnits(rung.icon);
		writeTypeProperties(
			properties,
			`--control-${size}-text`,
			resolveTypeRole(appearance, rung.text),
		);
	}
	return properties as AppearanceStyleProperties;
}

/** `8` becomes `calc(var(--spacing) * 8)`, matching Tailwind's own sizing utilities. */
function spacingUnits(multiple: string): string {
	return `calc(var(--spacing) * ${multiple})`;
}

function writeTypeProperties(
	properties: Record<string, string>,
	prefix: string,
	resolved: ResolvedTypeSlot,
): void {
	if (resolved.fontSize !== undefined)
		properties[`${prefix}-size`] = resolved.fontSize;
	if (resolved.fontWeight !== undefined)
		properties[`${prefix}-weight`] = resolved.fontWeight;
	if (resolved.lineHeight !== undefined)
		properties[`${prefix}-leading`] = resolved.lineHeight;
	if (resolved.letterSpacing !== undefined)
		properties[`${prefix}-tracking`] = resolved.letterSpacing;
	if (resolved.fontFamily !== undefined)
		properties[`${prefix}-family`] =
			resolved.fontFamily === "heading"
				? "var(--font-heading)"
				: resolved.fontFamily === "mono"
					? "var(--font-mono)"
					: "var(--font-sans)";
}

export function readableForeground(hexColor: string): "#000000" | "#ffffff" {
	const channels = [1, 3, 5].map(
		(offset) => Number.parseInt(hexColor.slice(offset, offset + 2), 16) / 255,
	);
	const linear = channels.map((channel) =>
		channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4,
	);
	const luminance =
		0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
	return luminance > 0.179 ? "#000000" : "#ffffff";
}
