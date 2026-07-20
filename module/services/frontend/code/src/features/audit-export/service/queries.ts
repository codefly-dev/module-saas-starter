import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { AuditExportService } from "@/gen/saas/accounts/v1/audit_export_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(AuditExportService, apiTransport);

export const auditExportQueries = {
	// config(orgId) returns the per-org export config or null when the
	// org has never configured one. The api responds with NotFound on
	// first visit; we translate that to a `null` data value so the FE
	// can branch cleanly on "not configured" without an error toast.
	config: (orgId: string) =>
		queryOptions({
			queryKey: ["audit-export", "config", orgId],
			queryFn: async () => {
				try {
					return await client.getConfig({ orgId });
				} catch (err) {
					if (err instanceof ConnectError && err.code === Code.NotFound) {
						return null;
					}
					throw err;
				}
			},
			enabled: !!orgId,
		}),
};
