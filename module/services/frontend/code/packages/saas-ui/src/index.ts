export { CollectionReadBoundary, CollectionGrants } from "./datasources/collection-access.js";
export { ConnectGitHubForm } from "./datasources/connect-github-form.js";
export {
	DatasourcesPanel,
	type DatasourcesPanelProps,
} from "./datasources/datasources-panel.js";
export {
	createDatasourceClient,
	datasourceClientOverTransport,
	type GatewayBinding,
} from "./datasources/gateway.js";
export {
	useAccessibleScopes,
	useAddGitHubSource,
	useDeleteSource,
	useListSources,
	useSyncSource,
} from "./datasources/queries.js";
export {
	type ConnectGitHubValues,
	connectGitHubSchema,
} from "./datasources/schema.js";
export type {
	AccessibleScopeView,
	CollectionAccessView,
	CollectionGrantView,
	CollectionGrantSubject,
	ConnectGitHubInput,
	DatasourceClient,
	DatasourceProviderName,
	DatasourceStatusName,
	DatasourceView,
} from "./datasources/types.js";
export { parsePaths } from "./datasources/util.js";
