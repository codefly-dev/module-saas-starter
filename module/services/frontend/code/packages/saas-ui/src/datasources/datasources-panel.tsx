"use client";

import {
	Badge,
	Button,
	Input,
	Label,
	Table,
	TableHeader,
	TableBody,
	TableHead,
	TableRow,
	TableCell,
} from "@codefly-dev/ui/layout";

import { ConnectError } from "@connectrpc/connect";

import {
	QueryClient,
	QueryClientProvider,
	useQuery,
} from "@tanstack/react-query";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import { CollectionGrants } from "./collection-access.js";
import { ConnectGitHubForm } from "./connect-github-form.js";
import { createDatasourceClient, type GatewayBinding } from "./gateway.js";
import {
	useAccessibleScopes,
	useAddGitHubSource,
	useDeleteSource,
	useListSources,
	useSyncSource,
} from "./queries.js";
import type { ConnectGitHubValues } from "./schema.js";
import type {
	AccessibleScopeView,
	DatasourceClient,
	DatasourceStatusName,
	DatasourceView,
} from "./types.js";
import {
	cn,
	formatGrants,
	formatIngest,
	formatSyncedAt,
	parsePaths,
	shortBoundaryId,
} from "./util.js";

interface DatasourcesPanelBaseProps {
	orgId: string;
	/** Called with the durable job id after a sync is enqueued. */
	onSyncEnqueued?: (jobId: string) => void;
	className?: string;
}

/**
 * Either drive the panel with an injected `client` (the portal adapts its own
 * transport, keeping its ambient `QueryClientProvider`), or hand it a `gateway`
 * binding and it self-wires: it builds the `@codefly-dev/saas-sdk` client and a
 * scoped React-Query provider from that binding, so a solution remote drops it
 * in with only `{ apiBase, getAccessToken }` and no query/auth context.
 */
export type DatasourcesPanelProps = DatasourcesPanelBaseProps &
	(
		| { client: DatasourceClient; gateway?: never }
		| { gateway: GatewayBinding; client?: never }
	);

export function DatasourcesPanel(props: DatasourcesPanelProps) {
	if (props.gateway) {
		const { gateway, ...rest } = props;
		return <GatewayBoundPanel gateway={gateway} {...rest} />;
	}
	const { client, ...rest } = props;
	return <DatasourcesPanelView client={client} {...rest} />;
}

function GatewayBoundPanel({
	gateway,
	...rest
}: DatasourcesPanelBaseProps & { gateway: GatewayBinding }) {
	// The interceptor reads the token at request time, so the client only needs
	// rebuilding when the binding itself changes — not on every render.
	const { apiBase, getAccessToken, refreshAccessToken, contentResource } =
		gateway;
	const client = useMemo(
		() =>
			createDatasourceClient({
				apiBase,
				getAccessToken,
				refreshAccessToken,
				contentResource,
			}),
		[apiBase, getAccessToken, refreshAccessToken, contentResource],
	);
	const [queryClient] = useState(
		() => new QueryClient({ defaultOptions: { queries: { retry: 1 } } }),
	);
	return (
		<QueryClientProvider client={queryClient}>
			<DatasourcesPanelView client={client} {...rest} />
		</QueryClientProvider>
	);
}

interface DatasourcesPanelViewProps extends DatasourcesPanelBaseProps {
	client: DatasourceClient;
}

function DatasourcesPanelView({
	client,
	orgId,
	onSyncEnqueued,
	className,
}: DatasourcesPanelViewProps) {
	const [activitySource, setActivitySource] = useState<DatasourceView | null>(
		null,
	);
	const [reconnecting, setReconnecting] = useState<DatasourceView | null>(null);
	const [reconnectPending, setReconnectPending] = useState(false);
	const [reconnectError, setReconnectError] = useState<string>();
	const beginAppSetup = client.beginGitHubAppSetup?.bind(client);
	const completeAppSetup = client.completeGitHubAppSetup?.bind(client);
	const migrateToApp = client.migrateGitHubSourceToApp?.bind(client);

	// The return leg, captured once during the first render: the redirect's
	// parameters are a property of the URL the page loaded with, so the value has
	// to survive the scrub below rather than be re-read from an address that no
	// longer carries them.
	//
	// Claimed only when this client can actually redeem them, and only for the
	// organization on screen when they landed. A consumer adapting its own client
	// may implement none of the App calls and handle the redirect itself, and must
	// keep both its parameters and its address bar; and the state is redeemable
	// only by the organization that began it, so re-firing it after an org switch
	// would report a rejection for a setup that in fact succeeded.
	const [appSetupReturn] = useState(() => {
		if (!completeAppSetup) return null;
		const params = readAppSetupReturn();
		return params && { ...params, orgId };
	});
	const [showConnect, setShowConnect] = useState(!!appSetupReturn);
	const [beginPending, setBeginPending] = useState(false);
	const [beginError, setBeginError] = useState<string>();
	// Repositories connected while this panel has been mounted, whatever the
	// method used. The completed setup's answer predates them and cannot be
	// re-asked, so this is the only record that they are now taken.
	const [connectedRepos, setConnectedRepos] = useState<ReadonlySet<string>>(
		() => new Set(),
	);
	// Per-row pending sets, not the shared mutation's single `isPending`, so two
	// rows can sync/delete at once without one clearing the other's spinner and
	// re-enabling a button whose request is still in flight (double-enqueue).
	const [syncingIds, setSyncingIds] = useState<ReadonlySet<string>>(
		() => new Set(),
	);
	const [deletingIds, setDeletingIds] = useState<ReadonlySet<string>>(
		() => new Set(),
	);
	const [migratingIds, setMigratingIds] = useState<ReadonlySet<string>>(
		() => new Set(),
	);
	// Row action errors have no other surface (no toast dependency, no global
	// mutation handler), so they would vanish silently without this.
	const [syncNotice, setSyncNotice] = useState<string | null>(null);
	const [actionError, setActionError] = useState<string | null>(null);

	const list = useListSources(client, orgId);
	const scopes = useAccessibleScopes(client, orgId);
	const collections = useQuery({
		queryKey: ["collection-access", orgId],
		queryFn: () => client.listCollections!(orgId),
		enabled: !!client.listCollections,
		retry: false,
		refetchInterval: 5000,
	});
	const [editingCollection, setEditingCollection] = useState<string>();
	const listedCollections = collections.isError ? undefined : collections.data;
	const selectedCollection = listedCollections?.find(
		(collection) => collection.nodeId === editingCollection,
	);
	const addMutation = useAddGitHubSource(client);
	const syncMutation = useSyncSource(client);
	const deleteMutation = useDeleteSource(client);

	const handleConnect = (values: ConnectGitHubValues) => {
		addMutation.mutate(
			{
				orgId,
				repo: values.repo,
				paths: parsePaths(values.paths),
				fileExtensions: parsePaths(values.fileExtensions).map((value) =>
					value.toLowerCase(),
				),
				branch: values.branch ?? "",
				targetCollection: values.targetCollection,
				boundaryNodeId: values.boundaryNodeId || undefined,
				accessToken: values.method === "app" ? undefined : values.accessToken,
				webhookSecret: values.webhookSecret ?? "",
			},
			{
				onSuccess: () => {
					setConnectedRepos((prev) => new Set(prev).add(values.repo));
					setShowConnect(false);
				},
			},
		);
	};

	const appSetupActive = !!appSetupReturn && appSetupReturn.orgId === orgId;
	const appSetup = useQuery({
		queryKey: [
			"github-app-setup",
			appSetupReturn?.orgId,
			appSetupReturn?.state,
		],
		queryFn: () =>
			completeAppSetup!(
				appSetupReturn!.orgId,
				appSetupReturn!.state,
				appSetupReturn!.installationId,
				appSetupReturn!.code,
			),
		enabled: appSetupActive,
		// The state is redeemable exactly once, so a retry or a background refetch
		// would report a rejection for a setup that in fact succeeded.
		retry: false,
		staleTime: Number.POSITIVE_INFINITY,
		refetchOnMount: false,
		refetchOnWindowFocus: false,
	});
	// Burn the state out of the address bar for the same reason, and to keep it out
	// of browser history and same-origin referrers.
	useEffect(() => {
		if (appSetupReturn) scrubAppSetupReturn();
	}, [appSetupReturn]);

	// `alreadyConnected` is answered once, when the setup completes, and that
	// answer can never be refreshed: the state behind it is redeemable exactly
	// once, so refetching would report a rejection for a setup that succeeded.
	// A repository connected since therefore has to be folded in here, or the
	// picker keeps offering one this organization already holds.
	const appRepositories = useMemo(() => {
		const granted = appSetupActive ? appSetup.data?.repositories : undefined;
		return granted?.map((candidate) =>
			candidate.alreadyConnected || !connectedRepos.has(candidate.repo)
				? candidate
				: { ...candidate, alreadyConnected: true },
		);
	}, [appSetupActive, appSetup.data, connectedRepos]);
	const appSetupPhase = beginPending
		? "beginning"
		: appSetupActive && appSetup.isFetching
			? "completing"
			: undefined;
	const appSetupError = beginError
		? beginError
		: appSetupActive && appSetup.isError
			? messageOf(appSetup.error)
			: undefined;

	const handleBeginAppSetup = async () => {
		if (!beginAppSetup) return;
		setBeginError(undefined);
		setBeginPending(true);
		try {
			const handle = await beginAppSetup(orgId);
			// Navigating away, so the pending flag is deliberately left set: the
			// button must not re-enable under a browser that is already unloading.
			window.location.assign(handle.installUrl);
		} catch (error) {
			setBeginError(messageOf(error));
			setBeginPending(false);
		}
	};

	const handleMigrateToApp = async (source: DatasourceView) => {
		if (!migrateToApp) return;
		setSyncNotice(null);
		setActionError(null);
		setMigratingIds((prev) => new Set(prev).add(source.id));
		try {
			await migrateToApp(orgId, source.id);
			setSyncNotice(
				`${source.repo} now authenticates through the GitHub App. Its stored token is no longer used.`,
			);
			await list.refetch();
		} catch (error) {
			setActionError(
				`Couldn't move ${source.repo} onto the GitHub App: ${messageOf(error)}`,
			);
		} finally {
			setMigratingIds((prev) => without(prev, source.id));
		}
	};

	const handleSync = async (source: DatasourceView) => {
		setSyncNotice(null);
		setActionError(null);
		setSyncingIds((prev) => new Set(prev).add(source.id));
		// Per-call callbacks on a shared mutation only observe the latest call.
		// Await each request so every row reports its result and clears pending.
		try {
			const jobId = await syncMutation.mutateAsync({ orgId, id: source.id });
			setSyncNotice(
				`Sync queued for ${source.repo}. Ingestion runs in the background; content will appear in the collection when ready.`,
			);
			onSyncEnqueued?.(jobId);
		} catch (error) {
			setActionError(`Couldn't sync ${source.repo}: ${messageOf(error)}`);
		} finally {
			setSyncingIds((prev) => without(prev, source.id));
		}
	};

	const handleDelete = (source: DatasourceView) => {
		const ok = window.confirm(
			`Delete the data source for ${source.repo}?\n\n` +
				"Its stored credentials are removed. This cannot be undone.",
		);
		if (!ok) return;
		setActionError(null);
		setDeletingIds((prev) => new Set(prev).add(source.id));
		deleteMutation.mutate(
			{ orgId, id: source.id },
			{
				onError: (error) =>
					setActionError(`Couldn't delete ${source.repo}: ${messageOf(error)}`),
				onSettled: () => setDeletingIds((prev) => without(prev, source.id)),
			},
		);
	};

	const sources = list.data ?? [];
	const boundaries = useMemo(() => {
		const byNode = new Map<string, AccessibleScopeView>();
		for (const scope of (scopes.isError ? [] : scopes.data) ?? [])
			byNode.set(scope.nodeId, scope);
		return byNode;
	}, [scopes.data, scopes.isError]);
	// The scope lookup answers for every node kind, so a grant on a solution node
	// or on a placed record would silence a headline that speaks about
	// collections. With the collection list in hand the question can be asked
	// exactly; without it the scope set is all there is to go on.
	const readableCollection = listedCollections
		? listedCollections.some((collection) => boundaries.has(collection.nodeId))
		: boundaries.size > 0;

	return (
		<div className={cn("space-y-4", className)}>
			<div className="flex items-center justify-end">
				<Button type="button" onClick={() => setShowConnect(true)}>
					Connect GitHub
				</Button>
			</div>

			{scopes.isError ? (
				<p role="alert">
					Couldn’t verify collection permissions. This does not mean there is no
					indexed content.
				</p>
			) : scopes.isSuccess && !readableCollection ? (
				<p role="status">
					No readable collection. Ask an organization administrator for read
					access. Connecting or syncing a source does not grant access.
				</p>
			) : null}
			{client.listCollections ? (
				<section aria-label="Collection access" className="space-y-2">
					{collections.isError ? (
						<p role="alert">
							Couldn’t inspect collection grants. Organization administrator
							access is required.
						</p>
					) : (
						listedCollections?.map((collection) => (
							<div key={collection.nodeId}>
								<span>
									{collection.label} ·{" "}
									{scopes.isError || !scopes.isSuccess
										? "Read permission unresolved"
										: boundaries.has(collection.nodeId)
											? "You can read this collection"
											: "You do not have read access"}{" "}
									· Readers:{" "}
									{collection.grants
										.map((grant) => grant.subjectLabel)
										.join(", ") || "No collection read grants"}
								</span>
								{client.grantCollectionRead &&
									client.revokeCollectionRead &&
									client.listGrantSubjects && (
										<Button
											type="button"
											variant="link"
											size="sm"
											onClick={() => setEditingCollection(collection.nodeId)}
										>
											Manage read grants for {collection.label}
										</Button>
									)}
							</div>
						))
					)}
				</section>
			) : (
				<Button
					type="button"
					variant="link"
					size="sm"
					onClick={() => window.location.assign("/admin/datasources")}
				>
					Manage collection read grants in the host (organization
					administrators)
				</Button>
			)}
			{selectedCollection && (
				<CollectionGrants
					client={client}
					orgId={orgId}
					collection={selectedCollection}
				/>
			)}
			{activitySource && (
				<SourceHistory
					client={client}
					orgId={orgId}
					source={activitySource}
					onClose={() => setActivitySource(null)}
				/>
			)}

			{syncNotice && (
				<p role="status" className="text-sm text-muted-foreground">
					{syncNotice}
				</p>
			)}
			{actionError && (
				<div
					role="alert"
					className="flex items-center justify-between gap-3 rounded-md border border-destructive/40 bg-destructive/10 px-4 py-2 text-sm text-destructive"
				>
					<span>{actionError}</span>
					<Button
						type="button"
						className="text-xs underline"
						onClick={() => setActionError(null)}
					>
						Dismiss
					</Button>
				</div>
			)}

			{list.isLoading ? (
				<PanelMessage>Loading data sources…</PanelMessage>
			) : list.isError ? (
				<PanelMessage tone="error">
					Couldn&apos;t load data sources. Retry shortly or check the service
					status.
				</PanelMessage>
			) : sources.length === 0 ? (
				<PanelMessage>
					No data sources connected. Connect a GitHub repository to start
					ingesting.
				</PanelMessage>
			) : (
				<SourcesTable
					onActivity={client.listActivity ? setActivitySource : undefined}
					onReconnect={(source) => {
						setReconnectError(undefined);
						setReconnecting(source);
					}}
					sources={sources}
					boundaries={boundaries}
					onMigrateToApp={migrateToApp ? handleMigrateToApp : undefined}
					migratingIds={migratingIds}
					permissionsResolved={scopes.isSuccess && !scopes.isError}
					syncingIds={syncingIds}
					deletingIds={deletingIds}
					onSync={handleSync}
					onDelete={handleDelete}
				/>
			)}

			{reconnecting && (
				<ReconnectSource
					source={reconnecting}
					pending={reconnectPending}
					error={reconnectError}
					onCancel={() => {
						if (!reconnectPending) setReconnecting(null);
					}}
					onSubmit={async (token) => {
						setReconnectPending(true);
						setReconnectError(undefined);
						try {
							const jobId = await client.syncSource(
								orgId,
								reconnecting.id,
								token,
							);
							setSyncNotice(
								`Credential replaced. Sync queued for ${reconnecting.repo}. Open History for ingestion results.`,
							);
							setReconnecting(null);
							onSyncEnqueued?.(jobId);
							await list.refetch();
						} catch (error) {
							setReconnectError(messageOf(error));
						} finally {
							setReconnectPending(false);
						}
					}}
				/>
			)}
			{showConnect && (
				<ConnectGitHubForm
					// Both legs or neither: an install the panel cannot redeem on the
					// way back strands the tenant on a completed GitHub install with
					// nothing to show for it.
					onBeginAppSetup={
						beginAppSetup && completeAppSetup ? handleBeginAppSetup : undefined
					}
					appRepositories={appRepositories}
					appSetupPhase={appSetupPhase}
					appSetupError={appSetupError}
					collections={collections.isError ? [] : collections.data}
					readableNodeIds={
						scopes.isSuccess && !scopes.isError
							? [...boundaries.keys()]
							: undefined
					}
					collectionError={collections.isError}
					onSubmit={handleConnect}
					onCancel={() => setShowConnect(false)}
					isPending={addMutation.isPending}
					errorMessage={
						addMutation.isError ? messageOf(addMutation.error) : undefined
					}
				/>
			)}
		</div>
	);
}

function PanelMessage({
	children,
	tone = "muted",
}: {
	children: ReactNode;
	tone?: "muted" | "error";
}) {
	return (
		<div
			className={cn(
				"rounded-lg border border-dashed px-6 py-12 text-center text-sm",
				tone === "error" ? "text-destructive" : "text-muted-foreground",
			)}
		>
			{children}
		</div>
	);
}

/**
 * The parameters GitHub appends to the App's configured setup URL when it sends
 * the browser back: the installation it claims was installed, and the state we
 * minted. Both are required — `setup_action` is deliberately not consulted, so
 * an existing installation gaining repositories (`update`) lands here exactly as
 * a first install does.
 *
 * Returns null under SSR, where the panel renders before any address exists.
 */
function readAppSetupReturn(): {
	state: string;
	installationId: string;
	code: string;
} | null {
	if (typeof window === "undefined") return null;
	const params = new URLSearchParams(window.location.search);
	const state = params.get("state");
	const installationId = params.get("installation_id");
	// `code` is deliberately not part of the trigger. It is absent when the App
	// was registered without "Request user authorization (OAuth) during
	// installation", and the host answers that with the error naming the setting
	// — which an operator can act on, where ignoring the return says nothing.
	return state && installationId
		? { state, installationId, code: params.get("code") ?? "" }
		: null;
}

function scrubAppSetupReturn(): void {
	const params = new URLSearchParams(window.location.search);
	for (const key of ["state", "installation_id", "setup_action", "code"])
		params.delete(key);
	const query = params.toString();
	window.history.replaceState(
		null,
		"",
		`${window.location.pathname}${query ? `?${query}` : ""}${window.location.hash}`,
	);
}

function without(set: ReadonlySet<string>, id: string): ReadonlySet<string> {
	const next = new Set(set);
	next.delete(id);
	return next;
}

function messageOf(error: unknown): string {
	const message =
		error instanceof ConnectError
			? error.rawMessage
			: error instanceof Error
				? error.message
				: "unexpected error";
	return message.replace(/^rpc error: code = \w+ desc = /, "");
}

/**
 * A row's clocks. At most one of them ticks for a given source: a github
 * source's ingest is advanced by the change-set compiler and never sets
 * `lastSyncedAt`, while the pulled providers advance `lastSyncedAt` alone. So
 * the "Never" is dropped whenever there is an ingest to show — left in, it
 * would sit above live provenance telling the reader a healthy source has
 * never synced.
 */
function LastSyncCell({ source }: { source: DatasourceView }) {
	const ingest = formatIngest(source.lastIngestedAt, source.lastIngestedCommit);
	return (
		<>
			{(source.lastSyncedAt || !ingest) && (
				<div>{formatSyncedAt(source.lastSyncedAt)}</div>
			)}
			{ingest && <div className="text-xs">{ingest}</div>}
		</>
	);
}

/**
 * How each status that is not `active` presents. Active is deliberately absent:
 * it is the state of nearly every row, so badging it too would bury the states
 * that need a reader under a column of noise — and it and `unknown` would then
 * differ by their label alone.
 */
const statusPresentation: Record<
	Exclude<DatasourceStatusName, "active">,
	{ label: string; variant: "outline" | "secondary" | "destructive" }
> = {
	paused: { label: "Paused", variant: "secondary" },
	degraded: { label: "Degraded", variant: "destructive" },
	unknown: { label: "Unknown", variant: "outline" },
};

/**
 * A source's lifecycle state and, once it has left active, the reason the host
 * published. Shown in the row rather than behind History because the tenant
 * never caused this state and so has no reason to go looking for it.
 *
 * The reason renders for whatever status carries one. The wire scopes it to
 * "why the source left active" rather than to one particular way of leaving,
 * so keying it to `degraded` would drop a paused source's explanation exactly
 * as this panel used to drop a degraded one.
 *
 * Degrading clears the source's reconcile schedule and an incremental delivery
 * never lifts it, so nothing resumes on its own: the line has to name Sync, or
 * a reader who fixes the cause waits for a pull that cannot come. It also says
 * what stopped, since a red badge alone reads as "your content is gone" and
 * would send them to re-ingest content that is still there.
 */
function StatusCell({ source }: { source: DatasourceView }) {
	const presentation =
		source.status === "active" ? undefined : statusPresentation[source.status];
	return (
		<div className="space-y-0.5">
			{presentation && (
				<Badge variant={presentation.variant}>{presentation.label}</Badge>
			)}
			{source.statusReason && <p className="text-xs">{source.statusReason}</p>}
			{source.status === "degraded" && (
				<p className="text-xs text-muted-foreground">
					Scheduled pulls have stopped; use Sync to retry once the cause is
					fixed. Content already ingested stays readable.
				</p>
			)}
		</div>
	);
}

const headerClass = "px-3 py-2 text-left font-medium text-muted-foreground";
const cellClass = "px-3 py-2 align-middle";

function SourcesTable({
	sources,
	boundaries,
	permissionsResolved,
	syncingIds,
	deletingIds,
	migratingIds,
	onSync,
	onDelete,
	onActivity,
	onReconnect,
	onMigrateToApp,
}: {
	sources: DatasourceView[];
	boundaries: ReadonlyMap<string, AccessibleScopeView>;
	permissionsResolved: boolean;
	syncingIds: ReadonlySet<string>;
	deletingIds: ReadonlySet<string>;
	migratingIds: ReadonlySet<string>;
	onSync: (source: DatasourceView) => void;
	onDelete: (source: DatasourceView) => void;
	onActivity?: (source: DatasourceView) => void;
	onReconnect: (source: DatasourceView) => void;
	onMigrateToApp?: (source: DatasourceView) => void;
}) {
	return (
		<div className="overflow-x-auto rounded-lg border">
			<Table className="w-full text-sm">
				<TableHeader className="border-b bg-muted/40">
					<TableRow>
						<TableHead className={headerClass}>Repository</TableHead>
						<TableHead className={headerClass}>Status</TableHead>
						<TableHead className={headerClass}>Paths</TableHead>
						<TableHead className={headerClass}>Branch</TableHead>
						<TableHead className={headerClass}>Boundary</TableHead>
						<TableHead className={headerClass}>Webhook</TableHead>
						<TableHead className={headerClass}>Last sync dispatch</TableHead>
						<TableHead className={cn(headerClass, "text-right")}>
							Actions
						</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{sources.map((source) => (
						<TableRow key={source.id} className="border-b last:border-0">
							<TableCell className={cn(cellClass, "font-mono")}>
								{source.repo}
							</TableCell>
							<TableCell className={cellClass}>
								<StatusCell source={source} />
							</TableCell>
							<TableCell className={cellClass}>
								{source.paths.length === 0 ? (
									<span className="text-muted-foreground">All</span>
								) : (
									source.paths.join(", ")
								)}
								{!!source.fileExtensions?.length && (
									<div className="text-xs text-muted-foreground">
										Only {source.fileExtensions.join(", ")}
									</div>
								)}
							</TableCell>
							<TableCell className={cellClass}>
								{source.branch || "default"}
							</TableCell>
							<TableCell className={cellClass}>
								<BoundaryCell
									nodeId={source.boundaryNodeId}
									permissionsResolved={permissionsResolved}
									scope={boundaries.get(source.boundaryNodeId)}
								/>
							</TableCell>
							<TableCell className={cellClass}>
								{source.webhookConfigured
									? "Signing secret configured"
									: "Not configured"}
							</TableCell>
							<TableCell className={cn(cellClass, "text-muted-foreground")}>
								<LastSyncCell source={source} />
							</TableCell>
							<TableCell className={cn(cellClass, "text-right")}>
								<div className="inline-flex gap-2">
									{source.provider === "github" && (
										<Button
											type="button"
											variant="outline"
											size="sm"
											onClick={() => onReconnect(source)}
										>
											Reconnect
										</Button>
									)}
									{onMigrateToApp && source.provider === "github" && (
										<Button
											type="button"
											variant="outline"
											size="sm"
											disabled={migratingIds.has(source.id)}
											onClick={() => onMigrateToApp(source)}
										>
											{migratingIds.has(source.id)
												? "Moving to the App…"
												: "Use GitHub App"}
										</Button>
									)}
									{onActivity && (
										<Button
											variant="outline"
											size="sm"
											onClick={() => onActivity(source)}
										>
											History
										</Button>
									)}
									<Button
										type="button"
										variant="outline"
										size="sm"
										disabled={syncingIds.has(source.id)}
										onClick={() => onSync(source)}
									>
										{syncingIds.has(source.id) ? "Syncing…" : "Sync"}
									</Button>
									<Button
										type="button"
										variant="outline"
										size="sm"
										className="text-destructive"
										disabled={deletingIds.has(source.id)}
										onClick={() => onDelete(source)}
									>
										{deletingIds.has(source.id) ? "Deleting…" : "Delete"}
									</Button>
								</div>
							</TableCell>
						</TableRow>
					))}
				</TableBody>
			</Table>
		</div>
	);
}

function BoundaryCell({
	permissionsResolved,
	nodeId,
	scope,
}: {
	nodeId: string;
	scope: AccessibleScopeView | undefined;
	permissionsResolved: boolean;
}) {
	if (scope) {
		return (
			<div className="space-y-0.5">
				<div>{scope.label || shortBoundaryId(nodeId)}</div>
				<div className="text-xs text-muted-foreground">
					{formatGrants(scope.actions)}
				</div>
			</div>
		);
	}
	return (
		<div>
			<div className="font-mono text-xs">{shortBoundaryId(nodeId)}</div>
			<p className="text-xs">
				{permissionsResolved ? "No read access" : "Read permission unresolved"}
			</p>
		</div>
	);
}

function SourceHistory({
	client,
	orgId,
	source,
	onClose,
}: {
	client: DatasourceClient;
	orgId: string;
	source: DatasourceView;
	onClose: () => void;
}) {
	const history = useQuery({
		queryKey: ["source-history", orgId, source.id],
		queryFn: () => client.listActivity!(orgId, source.id),
		refetchInterval: 15000,
	});
	const names: Record<string, string> = {
		"saas.datasource.source.synced": "Sync requested",
		"saas.datasource.source.added": "Source connected",
		"saas.datasource.credential.updated": "Credential replaced",
		"saas.datasource.change_set_compiled": "Files queued for ingestion",
		"saas.datasource.sync.completed": "Ingestion completed",
		"saas.datasource.sync.failed": "Sync attempt failed",
	};
	return (
		<section
			aria-label="Sync history"
			className="rounded-lg border p-4 space-y-3"
		>
			<div className="flex items-center justify-between">
				<h3 className="font-medium">Sync history · {source.repo}</h3>
				<Button variant="outline" size="sm" onClick={onClose}>
					Close history
				</Button>
			</div>
			<p className="text-xs text-muted-foreground">
				Sync requests, dispatched files, and ingestion results. History
				refreshes automatically.
			</p>
			{history.isPending ? (
				<p>Loading history…</p>
			) : history.error ? (
				<p role="alert">Could not load history: {messageOf(history.error)}</p>
			) : !history.data?.length ? (
				<p>No recorded activity yet.</p>
			) : (
				<ol className="space-y-3" style={{ maxHeight: 360, overflowY: "auto" }}>
					{history.data.map((e) => (
						<li key={e.id} className="border-t pt-3 text-sm">
							<div className="flex justify-between gap-3">
								<strong>{names[e.type] ?? e.type}</strong>
								<time className="text-xs text-muted-foreground">
									{e.at ? new Date(e.at).toLocaleString() : "Unknown time"}
								</time>
							</div>
							<p className="text-xs text-muted-foreground">
								Actor: {e.actor === source.id ? "Source sync worker" : e.actor}
							</p>
							<dl className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
								{Object.entries(e.fields)
									.filter(([k]) => k !== "solution")
									.map(([k, v]) => (
										<div key={k}>
											<dt className="inline text-muted-foreground">
												{k.replaceAll("_", " ")}:{" "}
											</dt>
											<dd className="inline break-all">{String(v)}</dd>
										</div>
									))}
							</dl>
						</li>
					))}
				</ol>
			)}
		</section>
	);
}

function ReconnectSource({
	source,
	pending,
	error,
	onSubmit,
	onCancel,
}: {
	source: DatasourceView;
	pending: boolean;
	error?: string;
	onSubmit: (token: string) => Promise<void>;
	onCancel: () => void;
}) {
	const [token, setToken] = useState("");
	return (
		<form
			aria-label="Reconnect GitHub source"
			className="space-y-3 rounded-lg border p-4"
			onSubmit={(event) => {
				event.preventDefault();
				if (!pending && token.trim()) {
					const replacement = token.trim();
					setToken("");
					void onSubmit(replacement);
				}
			}}
		>
			<h3 className="font-medium">Reconnect {source.repo}</h3>
			<p className="text-sm text-muted-foreground">
				Replace the saved PAT and start a sync. Your source, collection,
				content, and history are preserved.
			</p>
			<Label className="block text-sm">
				New GitHub PAT
				<Input
					type="password"
					autoComplete="new-password"
					required
					maxLength={4096}
					value={token}
					disabled={pending}
					onChange={(event) => setToken(event.target.value)}
				/>
			</Label>
			{error && <p role="alert">{error}</p>}
			<div className="flex gap-2">
				<Button type="submit" disabled={pending || !token.trim()}>
					{pending ? "Validating and reconnecting…" : "Reconnect and sync"}
				</Button>
				<Button
					type="button"
					variant="outline"
					size="sm"
					disabled={pending}
					onClick={onCancel}
				>
					Cancel
				</Button>
			</div>
		</form>
	);
}
