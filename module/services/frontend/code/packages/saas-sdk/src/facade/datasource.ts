import { type Client, createClient, type Transport } from "@connectrpc/connect";
import { DatasourceService } from "../gen/saas/accounts/v1/datasource_pb.js";

export type Datasource = Client<typeof DatasourceService>;

export function New(gw: Transport): Datasource {
	return createClient(DatasourceService, gw);
}
