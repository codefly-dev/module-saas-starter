import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import {
  UserSettingsService,
  type UserSettings,
} from "@/gen/saas-starter_api_grpc_pb";

const client = createClient(UserSettingsService, apiTransport);

export const userSettingsMutations = {
  // Partial update — pass only the keys to change. Nested objects
  // (email, notifications) are replaced wholesale by the api jsonb
  // merge, so callers must always send the full nested object on
  // any nested-key change.
  update: (patch: Partial<UserSettings>) =>
    client.update({ patch: patch as UserSettings }),
};
