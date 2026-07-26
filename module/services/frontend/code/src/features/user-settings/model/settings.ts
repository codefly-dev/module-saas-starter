import type { MessageInitShape } from "@bufbuild/protobuf";
import {
	createSettingsFieldFactory,
	createSettingsUpdate,
} from "@codefly/saas-settings";
import {
	ThemePreference,
	type UserSettings,
	type UserSettingsSchema,
} from "@/gen/saas/accounts/v1/user_settings_pb";

export type UserSettingsPatch = MessageInitShape<typeof UserSettingsSchema>;

export type UserSettingPath =
	| "appearance.theme"
	| "regional.locale"
	| "regional.timezone"
	| "regional.date_format"
	| "regional.time_format"
	| "email.product"
	| "email.marketing"
	| "email.security"
	| "email.weekly_digest"
	| "notifications.in_app"
	| "notifications.push"
	| "notifications.sound";

export interface UpdateUserSettings {
	patch?: UserSettingsPatch;
	clearPaths?: readonly UserSettingPath[];
}

const field = createSettingsFieldFactory<UserSettings, UserSettingsPatch>();

// Builds the generated request shape in one place so FieldMask clearing never
// leaks untyped strings into components.
export function createUserSettingsUpdate({
	patch = {},
	clearPaths = [],
}: UpdateUserSettings = {}) {
	return createSettingsUpdate(patch, clearPaths);
}

// Settings is the frontend counterpart of the Go usersettings.Fields catalog.
// Components never traverse optional protobuf parents or repeat defaults.
export const Settings = {
	appearance: {
		theme: field(
			"appearance.theme",
			ThemePreference.SYSTEM,
			(settings) => settings?.appearance?.theme,
			(theme) => ({ appearance: { theme } }),
		),
	},
	regional: {
		locale: field(
			"regional.locale",
			"en",
			(settings) => settings?.regional?.locale,
			(locale) => ({ regional: { locale } }),
		),
		timezone: field(
			"regional.timezone",
			"UTC",
			(settings) => settings?.regional?.timezone,
			(timezone) => ({ regional: { timezone } }),
		),
		dateFormat: field(
			"regional.date_format",
			"iso",
			(settings) => settings?.regional?.dateFormat,
			(dateFormat) => ({ regional: { dateFormat } }),
		),
		timeFormat: field(
			"regional.time_format",
			"24h",
			(settings) => settings?.regional?.timeFormat,
			(timeFormat) => ({ regional: { timeFormat } }),
		),
	},
	email: {
		product: field(
			"email.product",
			true,
			(settings) => settings?.email?.product,
			(product) => ({ email: { product } }),
		),
		marketing: field(
			"email.marketing",
			false,
			(settings) => settings?.email?.marketing,
			(marketing) => ({ email: { marketing } }),
		),
		security: field(
			"email.security",
			true,
			(settings) => settings?.email?.security,
			(security) => ({ email: { security } }),
		),
		weeklyDigest: field(
			"email.weekly_digest",
			true,
			(settings) => settings?.email?.weeklyDigest,
			(weeklyDigest) => ({ email: { weeklyDigest } }),
		),
	},
	notifications: {
		inApp: field(
			"notifications.in_app",
			true,
			(settings) => settings?.notifications?.inApp,
			(inApp) => ({ notifications: { inApp } }),
		),
		push: field(
			"notifications.push",
			false,
			(settings) => settings?.notifications?.push,
			(push) => ({ notifications: { push } }),
		),
		sound: field(
			"notifications.sound",
			false,
			(settings) => settings?.notifications?.sound,
			(sound) => ({ notifications: { sound } }),
		),
	},
} as const;
