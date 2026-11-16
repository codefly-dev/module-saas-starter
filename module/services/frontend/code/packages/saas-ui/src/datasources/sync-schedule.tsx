"use client";

import { Spinner, useLoadingPhase } from "@codefly-dev/ui/layout";
import { useListSources } from "./queries.js";
import type { DatasourceClient } from "./types.js";
import { formatSchedule } from "./util.js";

/**
 * How often each connected source refreshes on its own, one line per source.
 * Read from the same source list the panel shows, so the two never disagree.
 */
export function DatasourceSyncSchedule({
	client,
	orgId,
}: {
	client: DatasourceClient;
	orgId: string;
}) {
	const list = useListSources(client, orgId);
	const { indicator, quiet } = useLoadingPhase(list.isLoading);
	if (quiet) return null;
	if (indicator) return <Spinner label="Loading the schedule" size="sm" />;
	if (list.isError)
		return (
			<p role="alert" className="type-body text-destructive">
				Couldn’t load the schedule.
			</p>
		);
	const sources = list.data ?? [];
	if (sources.length === 0)
		return (
			<p className="type-body text-muted-foreground">
				No source is connected yet.
			</p>
		);
	return (
		<ul aria-label="Sync schedule" className="divide-y rounded-md border">
			{sources.map((source) => (
				<li
					key={source.id}
					className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 px-3 py-2"
				>
					<span className="type-body break-all">
						{source.repo || source.boundaryLabel}
					</span>
					<span className="type-caption-plain text-muted-foreground">
						{formatSchedule(source.reconcileIntervalSeconds) ||
							"Syncs when you press Sync now."}
						{(source.liveDelivery === "app_webhook" ||
							source.liveDelivery === "source_webhook") &&
							" Also updates on every push."}
					</span>
				</li>
			))}
		</ul>
	);
}
