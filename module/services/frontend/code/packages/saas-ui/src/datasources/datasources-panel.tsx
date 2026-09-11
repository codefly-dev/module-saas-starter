"use client";

import { ConnectError } from "@connectrpc/connect";

import {
	QueryClient,
	QueryClientProvider,
	useQuery,
} from "@tanstack/react-query";
import { type ReactNode, useMemo, useState } from "react";
import { ConnectGitHubForm } from "./connect-github-form.js";
import { createDatasourceClient, type GatewayBinding } from "./gateway.js";
import {
	useAddGitHubSource,
	useDeleteSource,
	useListSources,
	useSyncSource,
} from "./queries.js";
import type { ConnectGitHubValues } from "./schema.js";
import type { DatasourceClient, DatasourceView } from "./types.js";
import { cn, formatSyncedAt, parsePaths } from "./util.js";

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
	const { apiBase, getAccessToken, refreshAccessToken } = gateway;
	const client = useMemo(
		() =>
			createDatasourceClient({ apiBase, getAccessToken, refreshAccessToken }),
		[apiBase, getAccessToken, refreshAccessToken],
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

const buttonClass =
	"inline-flex h-9 items-center rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground shadow-sm hover:bg-primary/90 disabled:opacity-50";

const rowActionClass =
	"inline-flex h-8 items-center rounded-md border px-3 text-sm font-medium shadow-sm hover:bg-accent disabled:opacity-50";

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
	const [showConnect, setShowConnect] = useState(false);
	// Per-row pending sets, not the shared mutation's single `isPending`, so two
	// rows can sync/delete at once without one clearing the other's spinner and
	// re-enabling a button whose request is still in flight (double-enqueue).
	const [syncingIds, setSyncingIds] = useState<ReadonlySet<string>>(
		() => new Set(),
	);
	const [deletingIds, setDeletingIds] = useState<ReadonlySet<string>>(
		() => new Set(),
	);
	// Row action errors have no other surface (no toast dependency, no global
	// mutation handler), so they would vanish silently without this.
	const [syncNotice, setSyncNotice] = useState<string | null>(null);
	const [actionError, setActionError] = useState<string | null>(null);

	const list = useListSources(client, orgId);
	const addMutation = useAddGitHubSource(client);
	const syncMutation = useSyncSource(client);
	const deleteMutation = useDeleteSource(client);

	const handleConnect = (values: ConnectGitHubValues) => {
		addMutation.mutate(
			{
				orgId,
				repo: values.repo,
				paths: parsePaths(values.paths),
				branch: values.branch ?? "",
				targetCollection: values.targetCollection,
				accessToken: values.accessToken,
				webhookSecret: values.webhookSecret ?? "",
			},
			{ onSuccess: () => setShowConnect(false) },
		);
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
				`Sync queued for ${source.repo}. Ingestion runs in the background; documents will appear in Collection when ready.`,
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

	return (
		<div className={cn("space-y-4", className)}>
			<div className="flex items-center justify-end">
				<button
					type="button"
					className={buttonClass}
					onClick={() => setShowConnect(true)}
				>
					Connect GitHub
				</button>
			</div>

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
					<button
						type="button"
						className="text-xs underline"
						onClick={() => setActionError(null)}
					>
						Dismiss
					</button>
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
					onReconnect={(source) => { setReconnectError(undefined); setReconnecting(source); }}
					sources={sources}
					syncingIds={syncingIds}
					deletingIds={deletingIds}
					onSync={handleSync}
					onDelete={handleDelete}
				/>
			)}

			{reconnecting && (
				<ReconnectSource source={reconnecting} pending={reconnectPending} error={reconnectError}
				 onCancel={() => { if (!reconnectPending) setReconnecting(null); }}
				 onSubmit={async (token) => {
				  setReconnectPending(true); setReconnectError(undefined);
				  try {
				   const jobId = await client.syncSource(orgId, reconnecting.id, token);
				   setSyncNotice(`Credential replaced. Sync queued for ${reconnecting.repo}. Open History for ingestion results.`);
				   setReconnecting(null); onSyncEnqueued?.(jobId); await list.refetch();
				  } catch (error) { setReconnectError(messageOf(error)); }
				  finally { setReconnectPending(false); }
				 }} />
			)}
			{showConnect && (
				<ConnectGitHubForm
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

const headerClass = "px-3 py-2 text-left font-medium text-muted-foreground";
const cellClass = "px-3 py-2 align-middle";

function SourcesTable({
	sources,
	syncingIds,
	deletingIds,
	onSync,
	onDelete,
	onActivity,
	onReconnect,
}: {
	sources: DatasourceView[];
	syncingIds: ReadonlySet<string>;
	deletingIds: ReadonlySet<string>;
	onSync: (source: DatasourceView) => void;
	onDelete: (source: DatasourceView) => void;
	onActivity?: (source: DatasourceView) => void;
	onReconnect: (source: DatasourceView) => void;
}) {
	return (
		<div className="overflow-x-auto rounded-lg border">
			<table className="w-full text-sm">
				<thead className="border-b bg-muted/40">
					<tr>
						<th className={headerClass}>Repository</th>
						<th className={headerClass}>Paths</th>
						<th className={headerClass}>Branch</th>
						<th className={headerClass}>Webhook</th>
						<th className={headerClass}>Last sync dispatch</th>
						<th className={cn(headerClass, "text-right")}>Actions</th>
					</tr>
				</thead>
				<tbody>
					{sources.map((source) => (
						<tr key={source.id} className="border-b last:border-0">
							<td className={cn(cellClass, "font-mono")}>{source.repo}</td>
							<td className={cellClass}>
								{source.paths.length === 0 ? (
									<span className="text-muted-foreground">All</span>
								) : (
									source.paths.join(", ")
								)}
							</td>
							<td className={cellClass}>{source.branch || "default"}</td>
							<td className={cellClass}>
								{source.webhookConfigured ? "Signing secret configured" : "Not configured"}
							</td>
							<td className={cn(cellClass, "text-muted-foreground")}>
								<span title="Last sync dispatch. Open History for ingestion results.">
									{formatSyncedAt(source.lastSyncedAt)}
								</span>
							</td>
							<td className={cn(cellClass, "text-right")}>
								<div className="inline-flex gap-2">
									{source.provider === "github" && <button type="button" className={rowActionClass} onClick={() => onReconnect(source)}>Reconnect</button>}
									{onActivity && (
										<button
											className={rowActionClass}
											onClick={() => onActivity(source)}
										>
											History
										</button>
									)}
									<button
										type="button"
										className={rowActionClass}
										disabled={syncingIds.has(source.id)}
										onClick={() => onSync(source)}
									>
										{syncingIds.has(source.id) ? "Syncing…" : "Sync"}
									</button>
									<button
										type="button"
										className={cn(rowActionClass, "text-destructive")}
										disabled={deletingIds.has(source.id)}
										onClick={() => onDelete(source)}
									>
										{deletingIds.has(source.id) ? "Deleting…" : "Delete"}
									</button>
								</div>
							</td>
						</tr>
					))}
				</tbody>
			</table>
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
				<button className={rowActionClass} onClick={onClose}>
					Close history
				</button>
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
							<p className="text-xs text-muted-foreground">Actor: {e.actor === source.id ? "Source sync worker" : e.actor}</p>
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

function ReconnectSource({source, pending, error, onSubmit, onCancel}: {
 source: DatasourceView; pending: boolean; error?: string;
 onSubmit: (token: string) => Promise<void>; onCancel: () => void;
}) {
 const [token, setToken] = useState("");
 return <form aria-label="Reconnect GitHub source" className="space-y-3 rounded-lg border p-4"
 onSubmit={(event) => { event.preventDefault(); if (!pending && token.trim()) { const replacement = token.trim(); setToken(""); void onSubmit(replacement); } }}>
 <h3 className="font-medium">Reconnect {source.repo}</h3>
 <p className="text-sm text-muted-foreground">Replace the saved PAT and start a sync. Your source, collection, documents, and history are preserved.</p>
 <label className="block text-sm">New GitHub PAT<input type="password" autoComplete="new-password" required maxLength={4096} value={token} disabled={pending} onChange={(event) => setToken(event.target.value)} className="block w-full rounded-md border bg-background p-2" /></label>
 {error && <p role="alert">{error}</p>}
 <div className="flex gap-2"><button type="submit" className={buttonClass} disabled={pending || !token.trim()}>{pending ? "Validating and reconnecting…" : "Reconnect and sync"}</button><button type="button" className={rowActionClass} disabled={pending} onClick={onCancel}>Cancel</button></div>
 </form>;
}
