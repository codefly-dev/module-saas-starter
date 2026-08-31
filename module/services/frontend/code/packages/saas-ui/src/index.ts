export { ConnectGitHubForm } from "./datasources/connect-github-form.js";
export {
	DatasourcesPanel,
	type DatasourcesPanelProps,
} from "./datasources/datasources-panel.js";
export {
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
	ConnectGitHubInput,
	DatasourceClient,
	DatasourceProviderName,
	DatasourceStatusName,
	DatasourceView,
} from "./datasources/types.js";
export { parsePaths } from "./datasources/util.js";
