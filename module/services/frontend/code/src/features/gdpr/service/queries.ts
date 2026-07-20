import { createClient } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { GDPRService } from "@/gen/saas/accounts/v1/privacy_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(GDPRService, apiTransport);

export const gdprQueries = {
	exportStatus: (id: string) =>
		queryOptions({
			queryKey: ["gdpr", "export", id],
			queryFn: () => client.getExportStatus({ id }),
			enabled: !!id,
		}),

	deletionStatus: (id: string) =>
		queryOptions({
			queryKey: ["gdpr", "deletion", id],
			queryFn: () => client.getDeletionStatus({ id }),
			enabled: !!id,
		}),
};
