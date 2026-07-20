import { createClient } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { UserSettingsService } from "@/gen/saas/accounts/v1/user_settings_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(UserSettingsService, apiTransport);

export const userSettingsQueries = {
	current: () =>
		queryOptions({
			queryKey: ["user-settings"],
			queryFn: () => client.get({}),
			// Settings change rarely; the 30s default is fine, but bump to
			// 5 min so navigation between settings sub-pages doesn't refetch.
			staleTime: 5 * 60 * 1000,
		}),
};
