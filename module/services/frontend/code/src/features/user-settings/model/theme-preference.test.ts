import { describe, expect, it } from "vitest";
import { ThemePreference } from "@/gen/saas/accounts/v1/user_settings_pb";
import {
	isThemePreference,
	themePreferenceFromProto,
	themePreferenceToProto,
} from "./theme-preference";

describe("generated theme preference mapping", () => {
	it.each([
		["system", ThemePreference.SYSTEM],
		["light", ThemePreference.LIGHT],
		["dark", ThemePreference.DARK],
	] as const)("maps %s without free-form values", (name, generated) => {
		expect(themePreferenceToProto(name)).toBe(generated);
		expect(themePreferenceFromProto(generated)).toBe(name);
	});

	it("rejects unspecified and arbitrary browser values", () => {
		expect(
			themePreferenceFromProto(ThemePreference.UNSPECIFIED),
		).toBeUndefined();
		expect(isThemePreference("sepia")).toBe(false);
		expect(isThemePreference("system")).toBe(true);
	});
});
