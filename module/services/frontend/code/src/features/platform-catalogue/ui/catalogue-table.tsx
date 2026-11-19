"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { useMemo } from "react";
import type {
	CatalogueEntry,
	CatalogueInstallation,
} from "@/gen/saas/accounts/v1/platform_admin_pb";
import { formatDate } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
	Stack,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import {
	agentReleaseView,
	CATALOGUE_STATES,
	catalogueStates,
	kindLabel,
	publisherView,
	STATE_HEADINGS,
} from "../model/facts";
import { CatalogueFact, StateFacts, TeamGrants } from "./catalogue-facts";

const entryColumn = createColumnHelper<CatalogueEntry>();
const installationColumn = createColumnHelper<CatalogueInstallation>();

function organizations(count: number): string {
	return count === 1 ? "1 organization" : `${count} organizations`;
}

/**
 * The entries of one group, one row each: what it is, where it stands in each
 * state, and how many organizations have it installed. The count opens the
 * list of those organizations.
 */
export function CatalogueTable({
	entries,
	isLoading,
	onShowInstallations,
}: {
	entries: CatalogueEntry[];
	isLoading?: boolean;
	onShowInstallations: (entry: CatalogueEntry) => void;
}) {
	const columns = useMemo(
		() => [
			entryColumn.display({
				id: "name",
				header: "Name",
				cell: ({ row }) => (
					<Stack gap={2}>
						<Stack direction="row" gap={2} align="center">
							<span className="font-medium">{row.original.name}</span>
							<Badge variant="outline">{kindLabel(row.original.kind)}</Badge>
						</Stack>
						<CatalogueFact view={publisherView(row.original)} />
					</Stack>
				),
			}),
			...CATALOGUE_STATES.map((state) =>
				entryColumn.display({
					id: state,
					header: STATE_HEADINGS[state],
					cell: ({ row }) => (
						<StateFacts
							heading={STATE_HEADINGS[state]}
							facts={catalogueStates(row.original)[state]}
						/>
					),
				}),
			),
			entryColumn.display({
				id: "installations",
				header: "Installed by",
				cell: ({ row }) => {
					const count = row.original.installations.length;
					if (count === 0)
						return (
							<span className="text-sm text-muted-foreground">
								No organization
							</span>
						);
					return (
						<Button
							variant="outline"
							size="sm"
							aria-label={`Show the ${organizations(count)} using ${row.original.name}`}
							onClick={() => onShowInstallations(row.original)}
						>
							{organizations(count)}
						</Button>
					);
				},
			}),
		],
		[onShowInstallations],
	);
	const table = useReactTable({
		data: entries,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getRowId: (entry) => `${entry.kind}:${entry.name}`,
	});
	return (
		<DataTable
			table={table}
			isLoading={isLoading}
			emptyMessage="Nothing is deployed here."
		/>
	);
}

/** The organizations that have one entry installed, and which teams reach it. */
export function InstallationsDialog({
	entry,
	onClose,
}: {
	entry: CatalogueEntry;
	onClose: () => void;
}) {
	const columns = useMemo(
		() => [
			installationColumn.accessor("orgName", {
				header: "Organization",
				cell: (info) => <span className="font-medium">{info.getValue()}</span>,
			}),
			installationColumn.display({
				id: "release",
				header: "Release in use",
				cell: ({ row }) => (
					<CatalogueFact view={agentReleaseView(row.original)} />
				),
			}),
			installationColumn.display({
				id: "teams",
				header: "Teams with access",
				cell: ({ row }) => (
					<TeamGrants
						granted={row.original.grantedTeams}
						inherited={row.original.inheritedTeams}
					/>
				),
			}),
			installationColumn.display({
				id: "installed",
				header: "Installed on",
				cell: ({ row }) => (
					<span className="text-sm text-muted-foreground">
						{formatDate(
							row.original.installation?.createdAt
								? timestampDate(
										row.original.installation.createdAt,
									).toISOString()
								: undefined,
						)}
					</span>
				),
			}),
		],
		[],
	);
	const table = useReactTable({
		data: entry.installations,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getRowId: (item, index) => item.installation?.id ?? String(index),
	});
	return (
		<Dialog open onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="sm:max-w-3xl">
				<DialogHeader>
					<DialogTitle>Organizations using {entry.name}</DialogTitle>
					<DialogDescription>
						Each organization that has it installed, the release it runs and the
						teams that can reach it.
					</DialogDescription>
				</DialogHeader>
				<DataTable table={table} />
			</DialogContent>
		</Dialog>
	);
}
