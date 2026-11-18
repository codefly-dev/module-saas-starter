"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { History } from "lucide-react";
import type { JobAttempt } from "@/gen/saas/jobs/v1/jobs_pb";
import { formatDate } from "@/shared/lib/utils";
import { Badge, EmptyState } from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { jobFailureWords } from "../model/job-failure";
import { attemptOutcomeLabel, attemptOutcomeTone } from "../model/job-state";

const col = createColumnHelper<JobAttempt>();

const columns = [
	col.accessor("number", { header: "Try" }),
	col.accessor("startedAt", {
		header: "Started",
		cell: (info) => formatDate(info.getValue()),
	}),
	col.accessor("finishedAt", {
		header: "Finished",
		cell: (info) =>
			info.getValue() ? (
				formatDate(info.getValue())
			) : (
				<span className="text-muted-foreground">Not yet</span>
			),
	}),
	col.accessor("outcome", {
		header: "Result",
		cell: (info) => (
			<Badge tone={attemptOutcomeTone(info.getValue())} dot>
				{attemptOutcomeLabel(info.getValue())}
			</Badge>
		),
	}),
	col.display({
		id: "failure",
		header: "What went wrong",
		cell: ({ row }) =>
			row.original.failure ? (
				jobFailureWords(row.original.failure.code).summary
			) : (
				<span className="text-muted-foreground">Nothing</span>
			),
	}),
];

/** Every time the job was tried, in the order the server recorded them. */
export function JobAttemptsTable({ attempts }: { attempts: JobAttempt[] }) {
	const table = useReactTable({
		data: attempts,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getRowId: (row) => row.id,
	});
	if (attempts.length === 0) {
		return (
			<EmptyState
				className="py-6"
				icon={<History />}
				heading="Not tried yet"
				description="The job is still waiting for a worker to pick it up."
			/>
		);
	}
	return <DataTable table={table} emptyMessage="Not tried yet" />;
}
