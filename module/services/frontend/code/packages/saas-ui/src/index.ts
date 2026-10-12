export {
	CollectionGrants,
	CollectionReadBoundary,
} from "./datasources/collection-access.js";
export { ConnectGitHubForm } from "./datasources/connect-github-form.js";
export {
	DatasourcesPanel,
	type DatasourcesPanelProps,
} from "./datasources/datasources-panel.js";
// The credential block, presentational and controlled, exported for the same
// reason `matchDeclaredSources` is: a consumer that renders the three states
// in its own shell must be able to ASK for the credential without copying
// `ConnectGitHubForm`. `solutions/README.md` refuses a copied capability in a
// shared card, so offering the matching rule while withholding the one control
// a caller cannot write for itself would leave the only documented path out of
// the whole card a path across that boundary.
export {
	AccessTokenField,
	AppInstallPrompt,
	type CredentialMethod,
	CredentialMethodField,
	credentialMethodFrom,
	fieldErrorClass,
	WebhookSecretField,
} from "./datasources/credential-mode.js";
export {
	type DeclaredSource,
	declaredCollectionLabel,
	matchDeclaredSources,
} from "./datasources/declared-source.js";
export {
	DeclaredSourceCard,
	type DeclaredSourceCardProps,
} from "./datasources/declared-source-card.js";
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
	useReconcileSource,
} from "./datasources/queries.js";
// The effect identity a manual pass commits under, and the record of the
// passes this browser admitted. Exported because the host's own
// reconcile-schedules panel shows a person's pass beside the declared
// schedule's occurrences, which it can only do if the identity leaves the kit.
export {
	type AdmittedReconcilePass,
	createReconcilePassLedger,
	notifySourceReconcileAdmitted,
	onSourceReconcileAdmitted,
	type ReconcilePassLedger,
	type SourceReconcileStatus,
} from "./datasources/reconcile-passes.js";
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
	SourceReconcileReceipt,
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
	type BrowserTracing,
	type BrowserTracingOptions,
	type BrowserTracingStatus,
	COLLECTION_ACCESS_PATH,
	injectTraceContext,
	installBrowserTracing,
	type NameOf,
	NoReadableCollection,
	type NoReadableCollectionProps,
	OTEL_API_VERSION,
	PrincipalName,
	type PrincipalDirectoryState,
	type PrincipalNameProps,
	PrincipalNamesProvider,
	type PrincipalNamesProviderProps,
	requestBinding,
	resolveTraceExportEndpoint,
	type SolutionBinding,
	SolutionBindingError,
	type SolutionCredential,
	type SolutionRequestBinding,
	SolutionRequestError,
	type SolutionResource,
	solutionFetch,
	solutionJson,
	solutionTransport,
	TRACE_ATTRIBUTE_VALUE_MAX_LENGTH,
	TRACE_DENIED_ATTRIBUTE_KEYS,
	TRACE_EXPORT_ALLOWED_ORIGINS_BUILD_ENV,
	TRACE_EXPORT_BUILD_ENV,
	TRACE_EXPORT_DELAY_MS,
	TRACE_EXPORT_META_NAME,
	TRACE_RESOURCE_ATTRIBUTE_KEYS,
	TRACE_SPAN_NAME_PLACEHOLDER,
	TRACE_STANDARD_ATTRIBUTE_KEYS,
	useAccessibleScope,
	useAccessToken,
	useNameOf,
	usePrincipalDirectory,
	usePrincipalNames,
	useSolutionJson,
	useViewerEpoch,
	viewerAdministersOrganization,
	viewerIdentity,
	viewerOrganization,
	viewerPrincipal,
} from "./solution/index.js";
// When someone last signed in, said as a person reads it and kept true as time
// passes; relativeTime is its words alone, for a consumer's own shell.
export { LastLogin, type LastLoginProps, relativeTime } from "./audit/last-login.js";
