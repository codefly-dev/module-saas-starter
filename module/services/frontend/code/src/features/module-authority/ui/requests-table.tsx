"use client";

import { DescriptionList, Disclosure } from "@codefly-dev/ui/layout";
import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { useMemo } from "react";
import type { ModuleAuthorityRequest } from "@/gen/saas/accounts/v1/platform_admin_pb";
import { Badge, Button, Stack } from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { requestSummary } from "../model/draft";

const column = createColumnHelper<ModuleAuthorityRequest>();

/** The request, indented; shown as sent when it is not JSON. */
function indented(authority: string): string {
	try {
		return JSON.stringify(JSON.parse(authority), null, 2);
	} catch {
		return authority;
	}
}

/**
 * What each installed module asks to be allowed to do, and whether a platform
 * operator has approved it. One row per installed copy: two copies asking
 * under one name leave the platform with no single answer, so both are blocked
 * whatever was approved, and the row says so.
 */
export function RequestsTable({
	requested,
	isLoading,
	canRevoke,
	onRevoke,
}: {
	requested: ModuleAuthorityRequest[];
	isLoading: boolean;
	/** Whether this module's approval is one this page can take away. */
	canRevoke: (module: string) => boolean;
	onRevoke: (module: string) => void;
}) {
	const copies = useMemo(() => {
		const counted = new Map<string, number>();
		for (const request of requested) {
			counted.set(request.principal, (counted.get(request.principal) ?? 0) + 1);
		}
		return counted;
	}, [requested]);

	const columns = useMemo(
		() => [
			column.accessor("principal", {
				header: "Module",
				cell: (info) => <span className="font-medium">{info.getValue()}</span>,
			}),
			column.display({
				id: "asks",
				header: "What it asks to do",
				cell: ({ row }) => (
					<Stack gap={2}>
						{requestSummary(row.original.authority).map((line) => (
							<span key={line} className="text-sm">
								{line}
							</span>
						))}
					</Stack>
				),
			}),
			column.display({
				id: "status",
				header: "Status",
				cell: ({ row }) => {
					const installed = copies.get(row.original.principal) ?? 0;
					return (
						<Stack gap={2} align="start">
							{row.original.allowed ? (
								<Badge tone="success" dot>
									Approved
								</Badge>
							) : (
								<Badge tone="warning" dot>
									Waiting for approval
								</Badge>
							)}
							{installed > 1 && (
								<Badge tone="danger" dot>
									Blocked: installed {installed} times
								</Badge>
							)}
						</Stack>
					);
				},
			}),
			column.display({
				id: "actions",
				header: () => <span className="sr-only">Actions</span>,
				cell: ({ row }) =>
					row.original.allowed && canRevoke(row.original.principal) ? (
						<Button
							variant="outline"
							size="sm"
							aria-label={`Revoke approval for ${row.original.principal}`}
							onClick={() => onRevoke(row.original.principal)}
						>
							Revoke approval
						</Button>
					) : null,
			}),
		],
		[copies, canRevoke, onRevoke],
	);

	const table = useReactTable({
		data: requested,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getRowId: (request) => `${request.principal}:${request.binding}`,
	});

	return (
		<DataTable
			table={table}
			isLoading={isLoading}
			emptyMessage="No module is asking for permissions."
		/>
	);
}

/** Each request as it was sent, for the engineer who has to read one. */
export function RequestDetails({
	requested,
}: {
	requested: ModuleAuthorityRequest[];
}) {
	return (
		<Disclosure title="Technical details">
			<Stack gap={6}>
				{requested.map((request) => (
					<Stack key={`${request.principal}:${request.binding}`} gap={2}>
						<DescriptionList
							items={[
								{ term: "Module principal", value: request.principal },
								{ term: "Binding", value: request.binding },
								{ term: "Generation", value: request.generation.toString() },
								{ term: "Route alias", value: request.alias || "None" },
							]}
						/>
						<pre className="overflow-x-auto rounded-md bg-muted p-3 text-xs">
							{indented(request.authority)}
						</pre>
					</Stack>
				))}
			</Stack>
		</Disclosure>
	);
}
