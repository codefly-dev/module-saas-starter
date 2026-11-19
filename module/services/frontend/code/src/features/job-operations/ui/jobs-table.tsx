"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { ListChecks } from "lucide-react";
import { useMemo } from "react";
import type { JobSummary } from "@/gen/saas/jobs/v1/jobs_pb";
import { readOutcome, staleReadNotice } from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import { Banner, Button, EmptyState, Stack } from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { attemptsUsed, jobFailureWords } from "../model/job-failure";
import { JobScopeLabel } from "./job-scope-label";
import { JobStateBadge } from "./job-state-badge";
import { ReadFailure } from "./read-failure";

const col = createColumnHelper<JobSummary>();

// See queues-table.tsx: never a fresh `[]` per render.
const NO_JOBS: JobSummary[] = [];

export function JobsTable({
	jobs,
	isLoading,
	isError,
	error,
	onRetry,
	filtered,
	onClearFilters,
	onInspect,
	page,
	hasPrevious,
	hasNext,
	onPrevious,
	onNext,
}: {
	/** One page of jobs; absent while there is no answer. */
	jobs: JobSummary[] | undefined;
	isLoading: boolean;
	isError: boolean;
	error: unknown;
	onRetry: () => void;
	filtered: boolean;
	onClearFilters: () => void;
	onInspect: (job: JobSummary) => void;
	/** 1 for the first page. The server pages by token, so there is no total. */
	page: number;
	hasPrevious: boolean;
	hasNext: boolean;
	onPrevious: () => void;
	onNext: () => void;
}) {
	const columns = useMemo(
		() => [
			col.accessor("createdAt", {
				header: "Created",
				cell: (info) => (
					<span className="whitespace-nowrap">
						{formatDate(info.getValue())}
					</span>
				),
			}),
			col.accessor("topic", {
				header: "Work",
				cell: ({ row }) => (
					<div>
						<div className="font-medium">{row.original.topic}</div>
						<div className="text-muted-foreground">
							Queue: {row.original.queue}
						</div>
					</div>
				),
			}),
			col.display({
				id: "scope",
				header: "For",
				cell: ({ row }) => <JobScopeLabel job={row.original} />,
			}),
			col.accessor("state", {
				header: "State",
				cell: (info) => <JobStateBadge state={info.getValue()} />,
			}),
			col.display({
				id: "attempts",
				header: "Tries used",
				cell: ({ row }) =>
					attemptsUsed(row.original.attemptCount, row.original.maxAttempts),
			}),
			col.display({
				id: "failure",
				header: "Last failure",
				cell: ({ row }) =>
					row.original.lastFailure ? (
						jobFailureWords(row.original.lastFailure.code).summary
					) : (
						<span className="text-muted-foreground">None</span>
					),
			}),
			col.display({
				id: "actions",
				header: () => <span className="sr-only">Actions</span>,
				cell: ({ row }) => (
					<Button
						size="sm"
						variant="ghost"
						aria-label={`View details of the ${row.original.topic} job created ${formatDate(row.original.createdAt)}`}
						onClick={() => onInspect(row.original)}
					>
						View details
					</Button>
				),
			}),
		],
		[onInspect],
	);

	const table = useReactTable({
		data: jobs ?? NO_JOBS,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getRowId: (row) => row.id,
		// The server pages by token; the table only draws the page it is given.
		manualPagination: true,
	});

	const outcome = readOutcome(isError, error);
	if (isError && (outcome === "forbidden" || jobs === undefined)) {
		return <ReadFailure subject="the jobs" error={error} onRetry={onRetry} />;
	}

	const empty = !isLoading && jobs?.length === 0;
	const stale = staleReadNotice(outcome, "the jobs");
	return (
		<Stack gap={3}>
			{stale && <Banner tone="warning">{stale}</Banner>}
			{empty ? (
				filtered ? (
					<EmptyState
						icon={<ListChecks />}
						heading="No jobs match these filters"
						description="Try another state, or check the queue name: it must match exactly."
					>
						<Button variant="outline" onClick={onClearFilters}>
							Clear filters
						</Button>
					</EmptyState>
				) : (
					<EmptyState
						icon={<ListChecks />}
						heading={page > 1 ? "No more jobs" : "No jobs yet"}
						description="A job is one piece of background work, such as sending an email or syncing a data source. Each appears here once it is queued."
					/>
				)
			) : (
				<DataTable table={table} isLoading={isLoading} emptyMessage="No jobs" />
			)}
			{(hasPrevious || hasNext) && (
				<Stack
					direction="row"
					gap={2}
					align="center"
					justify="between"
					className="flex-wrap"
				>
					<span className="text-muted-foreground">Page {page}</span>
					<Stack direction="row" gap={2}>
						<Button
							variant="outline"
							size="sm"
							disabled={!hasPrevious}
							onClick={onPrevious}
						>
							Previous
						</Button>
						<Button
							variant="outline"
							size="sm"
							disabled={!hasNext}
							onClick={onNext}
						>
							Next
						</Button>
					</Stack>
				</Stack>
			)}
		</Stack>
	);
}
