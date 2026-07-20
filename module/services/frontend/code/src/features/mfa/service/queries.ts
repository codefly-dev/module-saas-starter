import { createClient } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { MFAService } from "@/gen/saas/accounts/v1/mfa_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(MFAService, apiTransport);

export const mfaQueries = {
	devices: () =>
		queryOptions({
			queryKey: ["mfa", "devices"],
			queryFn: () => client.listDevices({}),
		}),
};
