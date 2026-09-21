import { queryOptions } from "@tanstack/react-query";
import { createAccountsClients } from "@/gen/saas/accounts/v1/frontend_catalog";
import { apiTransport } from "@/lib/connect/transport";

const clients = createAccountsClients(apiTransport);

export const permissionQueries = {
	// The permission vocabulary as the service itself declares it, rather than
	// a list maintained beside it: a permission the browser shows is one some
	// RPC actually reads.
	serviceInfo: () =>
		queryOptions({
			queryKey: ["service-info"],
			queryFn: () => clients.IntrospectionService.getServiceInfo({}),
			staleTime: 5 * 60 * 1000,
		}),
};
