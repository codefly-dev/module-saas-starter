import { useQuery } from "@tanstack/react-query";
import { usePlatformAdminService } from "@/lib/hooks/use-api-client";

// Both reads route through PlatformAdminService (saas.accounts.v1), even though
// the message types live in saas.events.v1. They are payload-free: only counts,
// timings, and control-plane subscription metadata cross the boundary.

export function useEventOperations() {
	const service = usePlatformAdminService();
	return useQuery({
		queryKey: ["event-operations"],
		queryFn: () => service.getEventOperations({}),
		refetchInterval: 15_000,
	});
}

export function useEventSubscriptions() {
	const service = usePlatformAdminService();
	return useQuery({
		queryKey: ["event-subscriptions"],
		queryFn: () => service.listEventSubscriptions({}),
		refetchInterval: 15_000,
	});
}
