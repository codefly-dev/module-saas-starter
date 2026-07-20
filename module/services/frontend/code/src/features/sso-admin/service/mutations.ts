import { createClient } from "@connectrpc/connect";
import { SSOAdminService } from "@/gen/saas/accounts/v1/sso_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(SSOAdminService, apiTransport);

export const ssoMutations = {
	// startSetup mints a one-shot admin-portal URL via WorkOS (or a
	// stub link in dev). Caller is expected to redirect the browser
	// to the returned URL; the FE doesn't follow it itself because
	// WorkOS' return_url handshake bounces back to /admin/sso.
	startSetup: (orgId: string, returnUrl: string) =>
		client.startSetup({ orgId, returnUrl }),

	disable: (orgId: string) => client.disable({ orgId }),
};
