import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
	ThemePreference,
	UserSettingsSchema,
} from "@/gen/saas/accounts/v1/user_settings_pb";
import { createUserSettingsUpdate, Settings } from "./settings";

describe("typed frontend settings catalog", () => {
	it("resolves defaults through every missing parent", () => {
		const empty = create(UserSettingsSchema);

		expect(Settings.appearance.theme.get(empty)).toBe(ThemePreference.SYSTEM);
		expect(Settings.regional.locale.get(empty)).toBe("en");
		expect(Settings.email.product.get(empty)).toBe(true);
		expect(Settings.notifications.inApp.get(empty)).toBe(true);
		expect(empty.appearance).toBeUndefined();
		expect(empty.regional).toBeUndefined();
	});

	it("preserves explicit false instead of coalescing to true", () => {
		const settings = create(UserSettingsSchema, {
			email: { product: false },
			notifications: { inApp: false },
		});

		expect(Settings.email.product.get(settings)).toBe(false);
		expect(Settings.notifications.inApp.get(settings)).toBe(false);
	});

	it("preserves explicit empty strings instead of truthy-coalescing defaults", () => {
		const settings = create(UserSettingsSchema, {
			regional: {
				locale: "",
				timezone: "",
				dateFormat: "",
				timeFormat: "",
			},
		});

		expect(Settings.regional.locale.get(settings)).toBe("");
		expect(Settings.regional.timezone.get(settings)).toBe("");
		expect(Settings.regional.dateFormat.get(settings)).toBe("");
		expect(Settings.regional.timeFormat.get(settings)).toBe("");
	});

	it("resolves defaults through present but empty nested parents", () => {
		const settings = create(UserSettingsSchema, {
			appearance: {},
			regional: {},
			email: {},
			notifications: {},
		});

		expect(Settings.appearance.theme.get(settings)).toBe(ThemePreference.SYSTEM);
		expect(Settings.regional.locale.get(settings)).toBe("en");
		expect(Settings.email.product.get(settings)).toBe(true);
		expect(Settings.notifications.inApp.get(settings)).toBe(true);
	});

	it("builds typed nested patches without requiring parents", () => {
		expect(Settings.appearance.theme.patch(ThemePreference.DARK)).toEqual({
			appearance: { theme: ThemePreference.DARK },
		});
		expect(Settings.regional.timezone.patch("Europe/Paris")).toEqual({
			regional: { timezone: "Europe/Paris" },
		});
	});

	it("exports protobuf field-mask paths from the typed fields", () => {
		expect(Settings.appearance.theme.path).toBe("appearance.theme");
		expect(Settings.email.weeklyDigest.path).toBe("email.weekly_digest");
	});

	it("builds a typed reset mask without JSON null", () => {
		const request = createUserSettingsUpdate({
			clearPaths: [Settings.appearance.theme.path, Settings.email.product.path],
		});

		expect(request.patch.appearance).toBeUndefined();
		expect(request.clearMask?.paths).toEqual([
			"appearance.theme",
			"email.product",
		]);
	});

	it("keeps explicit zero-value patches alongside unrelated clear paths", () => {
		const request = createUserSettingsUpdate({
			patch: Settings.email.product.patch(false),
			clearPaths: [Settings.regional.locale.path],
		});

		expect(request.patch.email?.product).toBe(false);
		expect(request.clearMask?.paths).toEqual(["regional.locale"]);
	});
});
