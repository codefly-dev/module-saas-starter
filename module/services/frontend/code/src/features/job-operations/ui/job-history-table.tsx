"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { History } from "lucide-react";
import type { JobStateTransition } from "@/gen/saas/jobs/v1/jobs_pb";
import { formatDate } from "@/shared/lib/utils";
import { EmptyState } from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { jobFailureWords } from "../model/job-failure";
import { jobTransitionLabel } from "../model/job-state";

const col = createColumnHelper<JobStateTransition>();

const columns = [
	col.accessor("occurredAt", {
		header: "When",
		cell: (info) => formatDate(info.getValue()),
	}),
	col.display({
		id: "change",
		header: "Change",
		cell: ({ row }) =>
			jobTransitionLabel(row.original.fromState, row.original.toState),
	}),
	col.display({
		id: "reason",
		header: "Why",
		cell: ({ row }) =>
			row.original.failure ? (
				jobFailureWords(row.original.failure.code).summary
			) : (
				<span className="text-muted-foreground">Normal progress</span>
			),
	}),
];

/** Each change of state the job went through, as it was recorded. */
export function JobHistoryTable({
	transitions,
}: {
	transitions: JobStateTransition[];
}) {
	const table = useReactTable({
		data: transitions,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getRowId: (row) => row.sequence.toString(),
	});
	if (transitions.length === 0) {
		return (
			<EmptyState
				className="py-6"
				icon={<History />}
				heading="No history recorded"
				description="Changes of state appear here as the job moves along."
			/>
		);
	}
	return <DataTable table={table} emptyMessage="No history recorded" />;
}
