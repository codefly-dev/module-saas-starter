import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { UserSettingsService, UserSettingsSchema } from "@/gen/saas-starter_api_grpc_pb";

const client = createClient(UserSettingsService, apiTransport);

export const userSettingsMutations = {
  // Partial update — pass only the keys to change. Nested objects (email,
  // notifications) are replaced wholesale by the api jsonb merge, so callers must
  // always send the full nested object on any nested-key change. We accept a message
  // INIT SHAPE (plain nested objects, no $typeName) and `create` the message here so
  // callers don't have to construct protobuf messages by hand.
  update: (patch: MessageInitShape<typeof UserSettingsSchema>) =>
    client.update({ patch: create(UserSettingsSchema, patch) }),
};
