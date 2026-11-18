"use client";

import { KPIRow, type Metric, useLoadingPhase } from "@/shared/ui";
import { type QueueTotals, queueTiles } from "../model/queue-totals";

/** The four headline counts across every queue in the read. */
export function QueueSummaryTiles({
	totals,
	isLoading,
	forbidden,
}: {
	/** Absent while there is no answer; the tiles then draw a dash, not a zero. */
	totals: QueueTotals | undefined;
	isLoading: boolean;
	forbidden: boolean;
}) {
	const { indicator } = useLoadingPhase(isLoading);
	const metrics: Metric[] = queueTiles(forbidden ? undefined : totals).map(
		(tile) => ({
			...tile,
			...(indicator
				? { state: "loading" as const }
				: forbidden
					? { state: "forbidden" as const }
					: {}),
		}),
	);
	return <KPIRow metrics={metrics} />;
}
