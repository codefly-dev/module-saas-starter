import { type Client, createClient, type Transport } from "@connectrpc/connect";
import { AuditService } from "../gen/saas/accounts/v1/audit_pb.js";

export type Audit = Client<typeof AuditService>;

export function New(gw: Transport): Audit {
	return createClient(AuditService, gw);
}
