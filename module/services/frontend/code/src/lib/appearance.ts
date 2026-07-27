import {
	FRONTEND_APPEARANCE_TOKEN_NAMES,
	type FrontendAppearance,
	type FrontendAppearanceTokenName,
} from "@codefly/saas-plugin-contract";
import type { CSSProperties } from "react";

export type AppearanceStyleProperties = CSSProperties &
	Record<`--appearance-${string}`, string>;

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
	};
	for (const mode of ["light", "dark"] as const) {
		for (const token of FRONTEND_APPEARANCE_TOKEN_NAMES)
			properties[appearanceVariableName(mode, token)] = appearance[mode][token];
	}
	return properties as AppearanceStyleProperties;
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
