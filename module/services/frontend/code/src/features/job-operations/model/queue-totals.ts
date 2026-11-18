import type { JobQueueSnapshot } from "@/gen/saas/jobs/v1/jobs_pb";

export interface QueueTotals {
	ready: number;
	processing: number;
	failedForGood: number;
	stalled: number;
}

type Counted = Pick<
	JobQueueSnapshot,
	"ready" | "processing" | "deadLetter" | "expiredLeases"
>;

function sum(values: bigint[]): number {
	return Number(values.reduce((total, value) => total + value, BigInt(0)));
}

/** The counts across every queue in one read. */
export function queueTotals(queues: readonly Counted[]): QueueTotals {
	return {
		ready: sum(queues.map((queue) => queue.ready)),
		processing: sum(queues.map((queue) => queue.processing)),
		failedForGood: sum(queues.map((queue) => queue.deadLetter)),
		stalled: sum(queues.map((queue) => queue.expiredLeases)),
	};
}

export interface QueueTile {
	id: keyof QueueTotals;
	label: string;
	/** `null` when there is no answer: it draws a dash, never a zero. */
	value: number | null;
}

/**
 * The four headline numbers. With no answer (`undefined`) every value is
 * `null`: totals are sums over a read, and a row of zeros would read as four
 * healthy queues.
 */
export function queueTiles(totals: QueueTotals | undefined): QueueTile[] {
	return [
		{ id: "ready", label: "Waiting to start", value: totals?.ready ?? null },
		{
			id: "processing",
			label: "Running now",
			value: totals?.processing ?? null,
		},
		{
			id: "failedForGood",
			label: "Failed for good",
			value: totals?.failedForGood ?? null,
		},
		{
			id: "stalled",
			label: "Stalled mid-run",
			value: totals?.stalled ?? null,
		},
	];
}
