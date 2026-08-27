import { type Client, createClient, type Transport } from "@connectrpc/connect";
import { AuditService } from "../gen/saas/accounts/v1/audit_pb.js";

/**
 * The typed Connect clients a consumer imports. The data-graph tooling only
 * needs the audit surface today; further services join this facade as the
 * dashboard capability grows. `SaasClient["audit"]` structurally satisfies the
 * tooling's `AuditAggregateClient`, so it drops straight into `runDashboard`.
 */
export interface SaasClient {
	readonly audit: Client<typeof AuditService>;
}

export function createSaasClient(transport: Transport): SaasClient {
	return {
		audit: createClient(AuditService, transport),
	};
}
