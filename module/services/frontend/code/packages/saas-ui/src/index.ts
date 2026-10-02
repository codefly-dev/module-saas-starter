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
	DatasourceAccountLinks,
	DatasourceDirectoryPanel,
} from "./datasources/directory.js";
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
export { SourceExecutionRestricted } from "./datasources/source-execution-access.js";
export {
	SourceSyncProgress,
	useSourceSync,
} from "./datasources/sync-progress.js";
// The sync-progress model is exported beside its component so a consumer can
// render the same phases in its own shell — a compact line in a header, say —
// without re-deriving them from the phase names and getting the terminal cases
// (a done sync that handed nothing off, a failure that is still retrying)
// subtly wrong.
export {
	DEFAULT_STALL_AFTER_MS,
	type DescribeSyncOptions,
	describeSync,
	SYNC_STEPS,
	type SyncProgressCount,
	type SyncProgressReport,
	type SyncProgressState,
	type SyncProgressTone,
	type SyncStepName,
} from "./datasources/sync-progress-model.js";
export {
	notifySourceSyncRequested,
	onSourceSyncRequested,
} from "./datasources/sync-requests.js";
export type {
	AccessibleScopeView,
	AccountLinkHandle,
	AccountLinkView,
	CollectionAccessView,
	CollectionGrantSubject,
	CollectionGrantView,
	ConnectGitHubInput,
	DatasourceClient,
	DatasourceDirectoryView,
	DatasourceProviderName,
	DatasourceStatusName,
	DatasourceView,
	DomainView,
	GroupBindingView,
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
// When someone last signed in, said as a person reads it and kept true as time
// passes; relativeTime is its words alone, for a consumer's own shell.
export { LastLogin, type LastLoginProps, relativeTime } from "./audit/last-login.js";
