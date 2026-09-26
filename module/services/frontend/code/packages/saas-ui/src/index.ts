export {
	CollectionGrants,
	CollectionReadBoundary,
} from "./datasources/collection-access.js";
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
	CollectionGrantSubject,
	CollectionGrantView,
	ConnectGitHubInput,
	DatasourceClient,
	DatasourceProviderName,
	DatasourceStatusName,
	DatasourceView,
	SourceSyncFailureReasonName,
	SourceSyncPhaseName,
	SourceSyncTriggerName,
	SourceSyncView,
} from "./datasources/types.js";
export { parsePaths } from "./datasources/util.js";
// The whole solution surface, so the package root and
// `@codefly-dev/saas-ui/solution` name the same set. A partial root left
// `useAccessibleScope` off while re-exporting the `NoReadableCollection` that
// consumes its "none" answer, which made the README's "exported from the
// package root" false for half a documented pair.
export {
	type AccessibleScopeState,
	COLLECTION_ACCESS_PATH,
	NoReadableCollection,
	type NoReadableCollectionProps,
	requestBinding,
	type SolutionBinding,
	SolutionBindingError,
	type SolutionCredential,
	type SolutionRequestBinding,
	SolutionRequestError,
	type SolutionResource,
	solutionFetch,
	solutionJson,
	solutionTransport,
	useAccessibleScope,
	useAccessToken,
	useSolutionJson,
	useViewerEpoch,
	viewerAdministersOrganization,
	viewerIdentity,
	viewerOrganization,
} from "./solution/index.js";
