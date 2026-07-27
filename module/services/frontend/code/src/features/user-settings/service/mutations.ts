import { createClient } from "@connectrpc/connect";
import { UserSettingsService } from "@/gen/saas/accounts/v1/user_settings_pb";
import { apiTransport } from "@/lib/connect/transport";
import {
	createUserSettingsUpdate,
	type UpdateUserSettings,
} from "../model/settings";

const client = createClient(UserSettingsService, apiTransport);

export const userSettingsMutations = {
	// Nested fields merge recursively. clearPaths removes explicit overrides so
	// those fields inherit their catalog defaults; JSON null is never a setting.
	update: (input: UpdateUserSettings = {}) =>
		client.update(createUserSettingsUpdate(input)),
};
