// TODO: Replace with real Connect RPC client when MFAService is generated
// import { createClient } from "@connectrpc/connect";
// import { apiTransport } from "@/lib/connect/transport";
// import { MFAService } from "@/gen/user_pb";
// const client = createClient(MFAService, apiTransport);

export const mfaMutations = {
  setupTOTP: async () => {
    // TODO: Replace with real RPC call
    // return client.setupTOTP({});
    return {
      secret: "JBSWY3DPEHPK3PXP",
      provisioningUri:
        "otpauth://totp/SaaS%20Starter:user@example.com?secret=JBSWY3DPEHPK3PXP&issuer=SaaS%20Starter",
    };
  },

  verifyTOTP: async (code: string) => {
    // TODO: Replace with real RPC call
    // return client.verifyTOTP({ code });
    return { verified: true, deviceId: crypto.randomUUID() };
  },

  revokeDevice: async (deviceId: string) => {
    // TODO: Replace with real RPC call
    // return client.revokeDevice({ deviceId });
    return {};
  },

  generateBackupCodes: async () => {
    // TODO: Replace with real RPC call
    // return client.generateBackupCodes({});
    return {
      codes: [
        "a1b2c3d4",
        "e5f6g7h8",
        "i9j0k1l2",
        "m3n4o5p6",
        "q7r8s9t0",
        "u1v2w3x4",
        "y5z6a7b8",
        "c9d0e1f2",
      ],
    };
  },
};
