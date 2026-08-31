import { type DatasourceClient, datasourceClientOverTransport } from "@codefly/saas-ui";
import { apiTransport } from "@/lib/connect/transport";

// The portal drives the shared datasource components over its own transport —
// the one that injects the bearer token and does single-flight refresh-and-retry
// on 401. `@codefly/saas-ui` owns the DatasourceService client and the
// protobuf→view mapping, so the portal and solution remotes share one adapter.
export const datasourceClient: DatasourceClient =
	datasourceClientOverTransport(apiTransport);
