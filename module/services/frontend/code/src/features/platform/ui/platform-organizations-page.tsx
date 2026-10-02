"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Plus } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { OrganizationDetail } from "@/features/organizations/ui/organization-detail";
import { useAuth } from "@/lib/auth";
import {
	mayKeepRetainedRows,
	readOutcome,
	readOutcomeMessage,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import { Badge, Button, Input, Label, Switch } from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { useAllOrganizations } from "../service/queries";
import { CreateOrganizationDialog } from "./create-organization-dialog";

interface PlatformOrganizationRow {
	id: string;
	name: string;
	slug: string;
	memberCount: number;
	createdAt?: string;
	archived: boolean;
}

const col = createColumnHelper<PlatformOrganizationRow>();

/**
 * Every organization on the platform, for a platform administrator: find one,
 * open it, and manage it by hand — rename it, change its members and their
 * roles, delete it — without belonging to it.
 */
export function PlatformOrganizationsPage() {
	const { platformRole } = useAuth();
	const [search, setSearch] = useState("");
	const [query, setQuery] = useState("");
	const [includeArchived, setIncludeArchived] = useState(false);
	// A stack of the tokens that opened each page, so "Previous" needs no
	// token of its own from the server.
	const [pages, setPages] = useState<string[]>([""]);
	const [selected, setSelected] = useState<PlatformOrganizationRow | null>(
		null,
	);
	const [creating, setCreating] = useState(false);

	useEffect(() => {
		const timer = setTimeout(() => {
			setQuery(search.trim());
			setPages([""]);
		}, 250);
		return () => clearTimeout(timer);
	}, [search]);

	const pageToken = pages[pages.length - 1];
	const { data, isLoading, isError, error } = useAllOrganizations({
		query,
		includeArchived,
		pageToken,
		enabled: !!platformRole,
	});
	const outcome = readOutcome(isError, error);
	const withheld = !mayKeepRetainedRows(outcome);
	const stale = staleReadNotice(outcome, "the organizations");
	const rows: PlatformOrganizationRow[] = (
		withheld ? [] : (data?.organizations ?? [])
	).map((entry) => ({
		id: entry.organization?.id ?? "",
		name: entry.organization?.name ?? "",
		slug: entry.organization?.slug ?? "",
		memberCount: entry.memberCount,
		createdAt: entry.organization?.createdAt
			? timestampDate(entry.organization.createdAt).toISOString()
			: undefined,
		archived: entry.organization?.archivedAt !== undefined,
	}));

	const columns = useMemo(
		() => [
			col.accessor("name", {
				header: "Name",
				cell: ({ row }) => (
					<button
						type="button"
						className="font-medium underline-offset-4 hover:underline"
						onClick={() => setSelected(row.original)}
					>
						{row.original.name}
					</button>
				),
			}),
			col.accessor("slug", {
				header: "Slug",
				cell: (info) => (
					<span className="font-mono text-sm text-muted-foreground">
						{info.getValue()}
					</span>
				),
			}),
			col.accessor("memberCount", { header: "Members" }),
			col.accessor("createdAt", {
				header: "Created",
				cell: (info) => (
					<span className="text-sm text-muted-foreground">
						{formatDate(info.getValue())}
					</span>
				),
			}),
			col.accessor("archived", {
				header: "",
				cell: (info) =>
					info.getValue() ? <Badge variant="outline">Archived</Badge> : null,
			}),
		],
		[],
	);

	const table = useReactTable({
		data: rows,
		columns,
		getCoreRowModel: getCoreRowModel(),
	});

	// Any platform role may read the list (the server's floor is support);
	// writes are the server's to refuse, and it says why.
	if (!platformRole) {
		return (
			<div className="rounded-lg border p-6">
				<h1 data-slot="page-title" className="type-page-title">
					Platform administrator required
				</h1>
				<p className="mt-2 text-sm text-muted-foreground">
					The platform&apos;s organization list spans every tenant and is
					restricted to platform administrators.
				</p>
			</div>
		);
	}

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<div>
					<h1 data-slot="page-title" className="type-page-title">
						Organizations
					</h1>
					<p className="text-muted-foreground">
						Every organization on the platform, whether or not you belong to it.
					</p>
				</div>
				{platformRole === "super_admin" && (
					<Button onClick={() => setCreating(true)}>
						<Plus className="mr-2 h-4 w-4" />
						Create organization
					</Button>
				)}
			</div>

			<div className="flex flex-wrap items-center gap-4">
				<Input
					aria-label="Search organizations"
					placeholder="Search by name or slug"
					className="max-w-sm"
					value={search}
					onChange={(event) => setSearch(event.target.value)}
				/>
				<div className="flex items-center gap-2">
					<Switch
						id="include-archived"
						checked={includeArchived}
						onCheckedChange={(checked) => {
							setIncludeArchived(checked);
							setPages([""]);
						}}
					/>
					<Label htmlFor="include-archived">Show deleted</Label>
				</div>
			</div>

			{stale && (
				<p role="status" className="text-sm text-muted-foreground">
					{stale}
				</p>
			)}
			<DataTable
				table={table}
				isLoading={isLoading}
				emptyMessage={readOutcomeMessage(
					outcome,
					"the organizations",
					query ? "No organization matches." : "No organizations yet.",
				)}
			/>
			<div className="flex justify-end gap-2">
				<Button
					variant="outline"
					size="sm"
					disabled={pages.length <= 1}
					onClick={() => setPages((stack) => stack.slice(0, -1))}
				>
					Previous
				</Button>
				<Button
					variant="outline"
					size="sm"
					disabled={!data?.nextPageToken}
					onClick={() =>
						data?.nextPageToken &&
						setPages((stack) => [...stack, data.nextPageToken])
					}
				>
					Next
				</Button>
			</div>

			{selected && (
				<OrganizationDetail
					key={selected.id}
					// The refreshed row, so a rename shows here at once.
					organization={rows.find((row) => row.id === selected.id) ?? selected}
					source="platform"
					onClose={() => setSelected(null)}
					onDeleted={() => setSelected(null)}
				/>
			)}

			<CreateOrganizationDialog open={creating} onOpenChange={setCreating} />
		</div>
	);
}
