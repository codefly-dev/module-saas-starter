"use client";

import { type ReactNode, useState } from "react";
import { ConnectGitHubForm } from "./connect-github-form.js";
import {
	useAddGitHubSource,
	useDeleteSource,
	useListSources,
	useSyncSource,
} from "./queries.js";
import type { ConnectGitHubValues } from "./schema.js";
import type { DatasourceClient, DatasourceView } from "./types.js";
import { cn, formatSyncedAt, parsePaths } from "./util.js";

export interface DatasourcesPanelProps {
	client: DatasourceClient;
	orgId: string;
	/** Called with the durable job id after a sync is enqueued. */
	onSyncEnqueued?: (jobId: string) => void;
	className?: string;
}

const buttonClass =
	"inline-flex h-9 items-center rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground shadow-sm hover:bg-primary/90 disabled:opacity-50";

const rowActionClass =
	"inline-flex h-8 items-center rounded-md border px-3 text-sm font-medium shadow-sm hover:bg-accent disabled:opacity-50";

export function DatasourcesPanel({
	client,
	orgId,
	onSyncEnqueued,
	className,
}: DatasourcesPanelProps) {
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

	const handleSync = (source: DatasourceView) => {
		setActionError(null);
		setSyncingIds((prev) => new Set(prev).add(source.id));
		syncMutation.mutate(
			{ orgId, id: source.id },
			{
				onSuccess: (jobId) => onSyncEnqueued?.(jobId),
				onError: (error) =>
					setActionError(`Couldn't sync ${source.repo}: ${messageOf(error)}`),
				onSettled: () => setSyncingIds((prev) => without(prev, source.id)),
			},
		);
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
					sources={sources}
					syncingIds={syncingIds}
					deletingIds={deletingIds}
					onSync={handleSync}
					onDelete={handleDelete}
				/>
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
	return error instanceof Error && error.message
		? error.message
		: "unexpected error";
}

const headerClass = "px-3 py-2 text-left font-medium text-muted-foreground";
const cellClass = "px-3 py-2 align-middle";

function SourcesTable({
	sources,
	syncingIds,
	deletingIds,
	onSync,
	onDelete,
}: {
	sources: DatasourceView[];
	syncingIds: ReadonlySet<string>;
	deletingIds: ReadonlySet<string>;
	onSync: (source: DatasourceView) => void;
	onDelete: (source: DatasourceView) => void;
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
						<th className={headerClass}>Last sync</th>
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
								{source.webhookConfigured ? "Configured" : "None"}
							</td>
							<td className={cn(cellClass, "text-muted-foreground")}>
								{formatSyncedAt(source.lastSyncedAt)}
							</td>
							<td className={cn(cellClass, "text-right")}>
								<div className="inline-flex gap-2">
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
