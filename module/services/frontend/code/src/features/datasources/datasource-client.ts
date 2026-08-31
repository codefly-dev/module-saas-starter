import { timestampDate } from "@bufbuild/protobuf/wkt";
import { createClient } from "@connectrpc/connect";
import type { DatasourceClient, DatasourceView } from "@codefly/saas-ui";
import {
	type Datasource as DatasourceMessage,
	DatasourceProvider,
	DatasourceService,
	DatasourceStatus,
} from "@/gen/saas/accounts/v1/datasource_pb";
import { apiTransport } from "@/lib/connect/transport";

// Adapts the in-app generated DatasourceService Connect client to the transport-
// free contract `@codefly/saas-ui` drives. Once `@codefly/saas-sdk` exposes a
// datasource client, this adapter swaps to it and the components stay untouched.
const connect = createClient(DatasourceService, apiTransport);

export const datasourceClient: DatasourceClient = {
	async listSources(orgId) {
		const response = await connect.listSources({ orgId });
		return response.datasources.map(toDatasourceView);
	},
	async addGitHubSource(input) {
		await connect.addGitHubSource(input);
	},
	async syncSource(orgId, id) {
		const response = await connect.syncSource({ orgId, id });
		return response.jobId;
	},
	async deleteSource(orgId, id) {
		await connect.deleteSource({ orgId, id });
	},
};

function toDatasourceView(source: DatasourceMessage): DatasourceView {
	return {
		id: source.id,
		orgId: source.orgId,
		provider:
			source.provider === DatasourceProvider.GITHUB ? "github" : "unknown",
		repo: source.github?.repo ?? "",
		paths: source.github ? [...source.github.paths] : [],
		branch: source.github?.branch ?? "",
		targetCollection: source.targetCollection,
		webhookConfigured: source.webhookConfigured,
		status:
			source.status === DatasourceStatus.ACTIVE
				? "active"
				: source.status === DatasourceStatus.PAUSED
					? "paused"
					: "unknown",
		lastSyncedAt: source.lastSyncedAt
			? timestampDate(source.lastSyncedAt).toISOString()
			: undefined,
		createdAt: source.createdAt
			? timestampDate(source.createdAt).toISOString()
			: undefined,
	};
}
