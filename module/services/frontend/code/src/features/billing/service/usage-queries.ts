import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { createClient } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import {
	UsageBucketInterval,
	UsageService,
} from "@/gen/saas/accounts/v1/usage_pb";
import { apiTransport } from "@/lib/connect/transport";

const usageClient = createClient(UsageService, apiTransport);

export function useUsageMeters(orgId: string | null) {
	return useQuery({
		queryKey: ["usage-meters", orgId],
		queryFn: () => usageClient.listUsageMeters({ organizationId: orgId ?? "" }),
		enabled: !!orgId,
	});
}

export function useUsageHistory(
	orgId: string,
	meter: string,
	fromISO: string,
	toISO: string,
) {
	return useQuery({
		queryKey: ["usage-history", orgId, meter, fromISO, toISO, "day"],
		queryFn: () =>
			usageClient.getUsageHistory({
				organizationId: orgId,
				meter,
				from: timestampFromDate(new Date(fromISO)),
				to: timestampFromDate(new Date(toISO)),
				bucket: UsageBucketInterval.DAY,
			}),
		enabled: !!orgId && !!meter && !!fromISO && !!toISO,
	});
}
