import { useQuery } from "@tanstack/react-query";
import { useAPIKeyService } from "@/lib/hooks/use-api-client";

// The largest page the method serves.
const PAGE_SIZE = 100;

/** Every live key of the organization: the listing is followed to its end,
 * so a key past the first page can still be found and revoked. */
export function useAPIKeys(orgId: string | null) {
	const svc = useAPIKeyService();
	return useQuery({
		queryKey: ["api-keys", orgId],
		queryFn: async () => {
			const first = await svc.listAPIKeys({
				organizationId: orgId!,
				pageSize: PAGE_SIZE,
			});
			const keys = [...first.keys];
			let pageToken = first.nextPageToken;
			while (pageToken) {
				const page = await svc.listAPIKeys({
					organizationId: orgId!,
					pageSize: PAGE_SIZE,
					pageToken,
				});
				keys.push(...page.keys);
				pageToken = page.nextPageToken;
			}
			return keys;
		},
		enabled: !!orgId,
	});
}
