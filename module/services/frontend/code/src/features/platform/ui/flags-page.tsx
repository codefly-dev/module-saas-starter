"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { useMemo } from "react";
import { Badge } from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import type { FeatureFlag } from "../model/types";
import { useFeatureFlags } from "../service/queries";

const col = createColumnHelper<FeatureFlag>();

export function FlagsPage() {
	const { data: flags = [], isLoading } = useFeatureFlags();

	const columns = useMemo(
		() => [
			col.accessor("name", { header: "Name" }),
			col.accessor("description", {
				header: "Description",
				cell: (info) => (
					<span className="text-muted-foreground">
						{info.getValue() || "-"}
					</span>
				),
			}),
			col.accessor("enabled", {
				header: "Legacy state",
				cell: (info) => (
					<Badge variant={info.getValue() ? "default" : "secondary"}>
						{info.getValue() ? "Enabled" : "Disabled"}
					</Badge>
				),
			}),
			col.accessor("rolloutPercent", {
				header: "Legacy rollout",
				cell: (info) => (
					<span className="text-muted-foreground">
						{info.getValue() != null ? `${info.getValue()}%` : "100%"}
					</span>
				),
			}),
			col.accessor("targetOrgIds", {
				header: "Target Orgs",
				cell: (info) => {
					const ids = info.getValue();
					if (!ids || ids.length === 0) {
						return <span className="text-muted-foreground">All</span>;
					}
					return (
						<Badge variant="outline" className="text-xs">
							{ids.length} orgs
						</Badge>
					);
				},
			}),
		],
		[],
	);

	const table = useReactTable({
		data: flags as FeatureFlag[],
		columns,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});

	return (
		<div className="space-y-6">
			<div>
				<h1 className="text-2xl font-bold tracking-tight">
					Legacy Feature Flags
				</h1>
				<p className="text-muted-foreground">
					Read-only migration inventory. These values are not evaluated at
					runtime and will be imported into Unleash during cutover.
				</p>
			</div>

			<DataTable
				table={table}
				isLoading={isLoading}
				emptyMessage="No legacy feature flags"
			/>
		</div>
	);
}
