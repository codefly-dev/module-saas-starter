import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { AuditExportService } from "@/gen/saas-starter_api_grpc_pb";
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
        $typeName: "customers.AuditExportConfig",
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
