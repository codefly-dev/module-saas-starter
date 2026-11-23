import { createClient } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { ConsentService } from "@/gen/saas/accounts/v1/consent_pb";
import { WaitlistService } from "@/gen/saas/accounts/v1/waitlist_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(ConsentService, apiTransport);
const acquisition = createClient(WaitlistService, apiTransport);

// Neither read is retried. Both are asked on every page, by the consent
// notice, and a failed one already has an answer there (no optional purpose is
// permitted): asking again would only add to whatever refused the first.
export const consentQueries = {
	// What the signed-in person has accepted and permitted.
	status: () =>
		queryOptions({
			queryKey: ["consent", "status"],
			queryFn: () => client.getStatus({}),
			retry: false,
		}),

	// What a visitor is asked: the deployment's current consent policy, which
	// arrives with its acquisition status.
	acquisition: () =>
		queryOptions({
			queryKey: ["consent", "acquisition-status"],
			queryFn: () => acquisition.getAcquisitionStatus({}),
			retry: false,
		}),
};
