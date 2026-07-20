import { createClient } from "@connectrpc/connect";
import { AuditExportService } from "@/gen/saas/accounts/v1/audit_export_pb";
import { apiTransport } from "@/lib/connect/transport";
import type { AuditExportFormValues } from "../model/schemas";

const client = createClient(AuditExportService, apiTransport);

export const auditExportMutations = {
	// save submits the SaveConfig RPC. The wire shape carries the full
	// AuditExportConfig message, but only the user-editable fields
	// matter on the FE — the api ignores id / orgId / timestamps and
	// generates them itself. Passing secretAccessKey="" preserves the
	// existing stored secret (the api treats "" as a no-op rotation).
	save: (orgId: string, values: AuditExportFormValues) =>
		client.saveConfig({
			config: {
				$typeName: "saas.accounts.v1.AuditExportConfig",
				id: "",
				orgId,
				bucket: values.bucket,
				region: values.region ?? "us-east-1",
				endpoint: values.endpoint ?? "",
				prefix: values.prefix ?? "",
				accessKeyId: values.accessKeyId,
				secretAccessKey: values.secretAccessKey ?? "",
				cadenceMinutes: values.cadenceMinutes,
				enabled: values.enabled ?? true,
				lastError: "",
			},
		}),

	delete: (orgId: string) => client.deleteConfig({ orgId }),
};
