import type { FrontendThemePreference } from "@codefly/saas-plugin-contract";
import { ThemePreference } from "@/gen/saas/accounts/v1/user_settings_pb";

export const themePreferenceOptions = ["light", "dark", "system"] as const;

export function themePreferenceFromProto(
	preference: ThemePreference | undefined,
): FrontendThemePreference | undefined {
	switch (preference) {
		case ThemePreference.SYSTEM:
			return "system";
		case ThemePreference.LIGHT:
			return "light";
		case ThemePreference.DARK:
			return "dark";
		default:
			return undefined;
	}
}

export function themePreferenceToProto(
	preference: FrontendThemePreference,
): ThemePreference {
	switch (preference) {
		case "system":
			return ThemePreference.SYSTEM;
		case "light":
			return ThemePreference.LIGHT;
		case "dark":
			return ThemePreference.DARK;
	}
}

export function isThemePreference(
	preference: string | undefined,
): preference is FrontendThemePreference {
	return themePreferenceOptions.some((candidate) => candidate === preference);
}
