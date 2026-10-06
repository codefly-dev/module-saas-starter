import { useQuery } from "@tanstack/react-query";
import { usePlatformAdminService } from "@/lib/hooks/use-api-client";

// The Catalogue is one snapshot of the registry, the installations and the
// composed modules; it holds tens of entries, so it is read whole, not paged.
export function usePlatformCatalogue(includeTombstoned: boolean) {
	const service = usePlatformAdminService();
	return useQuery({
		queryKey: ["platform-catalogue", includeTombstoned],
		queryFn: () => service.listPlatformCatalogue({ includeTombstoned }),
	});
}
