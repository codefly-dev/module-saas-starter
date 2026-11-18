"use client";

import { DescriptionList, Disclosure } from "@codefly-dev/ui/layout";
import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { History } from "lucide-react";
import { useMemo } from "react";
import type {
	SourceTriggerDeclaration,
	TriggerChildContext,
	TriggerDelegationReference,
} from "@/gen/saas/accounts/v1/datasource_pb";
import { EmptyState, Section, Stack } from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { momentWords, permissionsWords, runKindWords } from "../model/words";

const column = createColumnHelper<TriggerChildContext>();

/**
 * The runs on record for one source: each time its work used someone's access.
 * This is the only record of one, so access that was stopped shows up as the
 * next run not being here at all.
 */
export function RunsSection({
	runs,
	isLoading,
	canRun,
}: {
	runs: TriggerChildContext[];
	isLoading: boolean;
	/** Whether "Run now" can do anything: some access is in use. */
	canRun: boolean;
}) {
	const columns = useMemo(
		() => [
			column.accessor("mintedAt", {
				header: "Started",
				cell: (info) => momentWords(info.getValue(), "Unknown"),
			}),
			column.accessor("module", { header: "Module" }),
			column.accessor("lookup", {
				header: "What it did",
				cell: (info) => runKindWords(info.getValue()),
			}),
			column.accessor("scopes", {
				header: "What it could do",
				cell: (info) => permissionsWords(info.getValue()),
			}),
		],
		[],
	);
	const table = useReactTable({
		data: runs,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getRowId: (run) => run.record,
	});
	return (
		<Section
			title="Recent runs"
			description="Each time this source's work ran with someone's access. A run that was refused leaves nothing here."
		>
			{!isLoading && runs.length === 0 ? (
				<EmptyState
					icon={<History />}
					heading="No runs yet"
					description={
						canRun
							? "Runs appear here once this source's work has run. Use Run now to start one without waiting for the schedule."
							: "Runs appear here once this source's work has run. Run now is unavailable until someone's access is set up above."
					}
				/>
			) : (
				<DataTable
					table={table}
					isLoading={isLoading}
					emptyMessage="No runs yet."
				/>
			)}
		</Section>
	);
}

/** The platform's own names for everything above, for an engineer. */
export function TechnicalDetails({
	declaration,
	accesses,
	runs,
}: {
	declaration: SourceTriggerDeclaration | undefined;
	accesses: TriggerDelegationReference[];
	runs: TriggerChildContext[];
}) {
	const schedule = declaration?.schedule;
	const none = "None";
	return (
		<Disclosure title="Technical details">
			<Stack gap={6}>
				<DescriptionList
					layout="stacked"
					items={[
						{ term: "Release", value: declaration?.release || none },
						{ term: "Operation", value: declaration?.operation || none },
						{ term: "Schedule expression", value: schedule?.cron || none },
						{ term: "Run input", value: schedule?.inputJson || none },
						{
							term: "Declaration fingerprint",
							value: declaration?.digest || none,
						},
						{
							term: "How the schedule is run today",
							value:
								"Nothing consumes this declaration yet, so the platform runs the schedule itself in the meantime.",
						},
					]}
				/>
				{accesses.map((access) => (
					<DescriptionList
						key={access.delegationId}
						layout="stacked"
						items={[
							{
								term: "Delegation",
								value: `${access.module}/${access.binding} (${access.delegationId})`,
							},
							{ term: "Owner", value: access.ownerPrincipalId || none },
							{ term: "Audience", value: access.audience || none },
							{ term: "Scopes", value: access.scopes.join(", ") || none },
						]}
					/>
				))}
				{runs.map((run) => (
					<DescriptionList
						key={run.record}
						layout="stacked"
						items={[
							{ term: "Run record", value: run.record },
							{
								term: "Delegation, audience and scopes",
								value: `${run.delegationId} · ${run.audience} · ${run.scopes.join(", ") || none}`,
							},
						]}
					/>
				))}
			</Stack>
		</Disclosure>
	);
}
