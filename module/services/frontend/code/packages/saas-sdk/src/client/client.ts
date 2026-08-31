import type { Transport } from "@connectrpc/connect";
import * as audit from "../facade/audit.js";

/**
 * The typed Connect clients a consumer imports. The data-graph tooling only
 * needs the audit surface today; further services join this facade as the
 * dashboard capability grows. `SaasClient["audit"]` structurally satisfies the
 * tooling's `AuditAggregateClient`, so it drops straight into `runDashboard`.
 */
export interface SaasClient {
	readonly audit: audit.Audit;
}

export function createSaasClient(transport: Transport): SaasClient {
	return {
		audit: audit.New(transport),
	};
}
