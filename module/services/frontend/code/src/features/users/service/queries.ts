import { createClient } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { UserService } from "@/gen/saas/accounts/v1/identity_pb";
import { PlatformAdminService } from "@/gen/saas/accounts/v1/platform_admin_pb";
import { apiTransport } from "@/lib/connect/transport";

const adminClient = createClient(PlatformAdminService, apiTransport);
const userClient = createClient(UserService, apiTransport);

export const userQueries = {
	list: (query?: string) =>
		queryOptions({
			queryKey: ["users", query ?? ""],
			queryFn: () =>
				adminClient.searchUsers({ query: query ?? "", pageSize: 50 }),
		}),

	detail: (uuid: string) =>
		queryOptions({
			queryKey: ["users", uuid],
			queryFn: () =>
				userClient.getUser({ identifier: { case: "uuid", value: uuid } }),
			enabled: !!uuid,
		}),
};
