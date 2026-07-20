import { createClient } from "@connectrpc/connect";
import { MFAService } from "@/gen/saas/accounts/v1/mfa_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(MFAService, apiTransport);

export const mfaMutations = {
	beginWebAuthnRegistration: () => client.beginWebAuthnRegistration({}),

	finishWebAuthnRegistration: (
		ceremonyToken: string,
		credentialResponseJson: string,
		name: string,
	) =>
		client.finishWebAuthnRegistration({
			ceremonyToken,
			credentialResponseJson,
			name,
		}),

	setupTOTP: () => client.setupTOTP({}),

	verifyTOTP: (code: string) => client.verifyTOTP({ code }),

	revokeDevice: (id: string) => client.revokeDevice({ id }),

	generateBackupCodes: () => client.generateBackupCodes({}),
};
