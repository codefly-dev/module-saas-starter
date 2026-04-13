import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { MFAService } from "@/gen/user_pb";

const client = createClient(MFAService, apiTransport);

export const mfaMutations = {
  setupTOTP: () =>
    client.setupTOTP({}),

  verifyTOTP: (code: string) =>
    client.verifyTOTP({ code }),

  revokeDevice: (id: string) =>
    client.revokeDevice({ id }),

  generateBackupCodes: () =>
    client.generateBackupCodes({}),
};
