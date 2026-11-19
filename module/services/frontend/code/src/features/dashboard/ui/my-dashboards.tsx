"use client";

import {
	type ColumnDef,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { ArrowLeft, MoreHorizontal, Plus } from "lucide-react";
import { useMemo, useState } from "react";
import { useAuth } from "@/lib/auth";
import { failureText } from "@/shared/lib/failure-text";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	Badge,
	Button,
	Card,
	CardContent,
	CardHeader,
	CardTitle,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
	ErrorState,
	Field,
	Input,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import type { DashboardRecord } from "../model/record";
import {
	createBrowserDashboardLibrary,
	type DashboardLibrary,
	dashboardRecordStore,
} from "../service/dashboard-library";
import {
	scopedDashboardDraftKey,
	USER_DASHBOARD_LIBRARY_KEY,
} from "../service/use-dashboard-authoring";
import {
	emptyDashboardSpec,
	useDashboardLibrary,
} from "../service/use-dashboard-library";
import { DashboardEditor } from "./dashboard-editor";

function formatUpdated(iso: string): string {
	const parsed = new Date(iso);
	return Number.isNaN(parsed.getTime())
		? "Date unavailable"
		: parsed.toLocaleDateString(undefined, {
				year: "numeric",
				month: "short",
				day: "numeric",
			});
}

export function MyDashboards({ library }: { library?: DashboardLibrary }) {
	const { user, organizationId } = useAuth();
	const scopedKey = scopedDashboardDraftKey(USER_DASHBOARD_LIBRARY_KEY, {
		organizationId,
		userId: user?.id,
	});
	return (
		<DashboardCollection
			key={scopedKey}
			library={library}
			scopedKey={scopedKey}
		/>
	);
}

function DashboardCollection({
	library,
	scopedKey,
}: {
	library?: DashboardLibrary;
	scopedKey: string;
}) {
	const defaultLibrary = useMemo(
		() => createBrowserDashboardLibrary(scopedKey),
		[scopedKey],
	);
	const activeLibrary = library ?? defaultLibrary;
	const {
		records,
		error,
		readError,
		isLoading,
		create,
		rename,
		setVisibility,
		duplicate,
		remove,
	} = useDashboardLibrary(scopedKey, activeLibrary);
	const [openId, setOpenId] = useState<string | null>(null);
	const openRecord = records.find((record) => record.id === openId) ?? null;
	const recordStore = useMemo(
		() => (openId ? dashboardRecordStore(activeLibrary, openId) : null),
		[activeLibrary, openId],
	);
	const columns = useMemo<ColumnDef<DashboardRecord>[]>(
		() => [
			{ accessorKey: "name", header: "Dashboard" },
			{
				id: "updated",
				header: "Updated",
				accessorFn: (record) => formatUpdated(record.updatedAt),
			},
			{
				id: "visibility",
				header: "Visibility",
				cell: ({ row }) => (
					<Badge variant="secondary">
						{!library
							? "This browser"
							: row.original.visibility === "org"
								? "Shared"
								: "Private"}
					</Badge>
				),
			},
			{
				id: "actions",
				header: "",
				cell: ({ row }) => (
					<DashboardActions
						record={row.original}
						canShare={!!library}
						onOpen={() => setOpenId(row.original.id)}
						onRename={(name) => rename(row.original.id, name)}
						onDuplicate={() => duplicate(row.original.id)}
						onShare={() =>
							setVisibility(
								row.original.id,
								row.original.visibility === "org" ? "private" : "org",
							)
						}
						onDelete={() => remove(row.original.id)}
					/>
				),
			},
		],
		[library, rename, duplicate, setVisibility, remove],
	);
	const table = useReactTable({
		data: records,
		columns,
		getCoreRowModel: getCoreRowModel(),
	});

	if (readError)
		return (
			<ErrorState
				title={
					library
						? failureText("view your dashboards", readError)
						: "Your dashboards could not be loaded."
				}
				detail={
					library
						? undefined
						: "Check your browser’s storage settings, then reload the page. Your saved dashboards have not been replaced."
				}
			/>
		);
	if (openRecord && recordStore)
		return (
			<div className="space-y-4">
				<div className="flex flex-wrap items-center gap-2">
					<Button variant="ghost" size="sm" onClick={() => setOpenId(null)}>
						<ArrowLeft className="mr-1 size-4" />
						All dashboards
					</Button>
					<span className="truncate font-medium">{openRecord.name}</span>
				</div>
				<DashboardEditor
					storageKey={openRecord.id}
					initial={openRecord.spec}
					store={recordStore}
				/>
			</div>
		);
	return (
		<div className="space-y-6">
			<CreateDashboardForm
				disabled={isLoading}
				onCreate={async (name) => {
					try {
						const record = await create({
							name,
							spec: emptyDashboardSpec(name),
						});
						setOpenId(record.id);
						return true;
					} catch {
						return false;
					}
				}}
			/>
			{!library && (
				<p className="text-sm text-muted-foreground">
					These dashboards are saved in this browser. Sharing with other people
					is not available yet.
				</p>
			)}
			{error && (
				<ErrorState
					title={failureText("save the dashboard change", error)}
					detail={
						library
							? undefined
							: "Your previous saved version remains available. Check your browser’s storage settings before trying again."
					}
				/>
			)}
			<DataTable
				table={table}
				isLoading={isLoading}
				emptyMessage="No dashboards yet. Name your first dashboard above to start building."
			/>
		</div>
	);
}

function CreateDashboardForm({
	onCreate,
	disabled,
}: {
	onCreate: (name: string) => Promise<boolean>;
	disabled: boolean;
}) {
	const [name, setName] = useState("");
	const [pending, setPending] = useState(false);
	return (
		<Card>
			<CardHeader>
				<CardTitle>New dashboard</CardTitle>
			</CardHeader>
			<CardContent>
				<form
					className="flex flex-col gap-2 sm:flex-row sm:items-end"
					onSubmit={async (event) => {
						event.preventDefault();
						if (!name.trim() || pending) return;
						setPending(true);
						try {
							if (await onCreate(name.trim())) setName("");
						} finally {
							setPending(false);
						}
					}}
				>
					<Field label="Name" className="flex-1">
						{(control) => (
							<Input
								{...control}
								value={name}
								disabled={disabled || pending}
								placeholder="Weekly activity"
								onChange={(event) => setName(event.target.value)}
							/>
						)}
					</Field>
					<Button type="submit" disabled={disabled || pending || !name.trim()}>
						<Plus className="mr-1 size-4" />
						{pending ? "Creating…" : "Create"}
					</Button>
				</form>
			</CardContent>
		</Card>
	);
}

function DashboardActions({
	record,
	canShare,
	onOpen,
	onRename,
	onDuplicate,
	onShare,
	onDelete,
}: {
	record: DashboardRecord;
	canShare: boolean;
	onOpen: () => void;
	onRename: (name: string) => Promise<boolean>;
	onDuplicate: () => Promise<DashboardRecord | null>;
	onShare: () => Promise<boolean>;
	onDelete: () => Promise<boolean>;
}) {
	const [renaming, setRenaming] = useState(false);
	const [deleting, setDeleting] = useState(false);
	const [pending, setPending] = useState(false);
	const [name, setName] = useState(record.name);
	const [failure, setFailure] = useState<string>();
	const run = async (action: () => Promise<unknown>, done?: () => void) => {
		if (pending) return;
		setPending(true);
		setFailure(undefined);
		try {
			if (await action()) done?.();
			else
				setFailure(
					"This change could not be saved. Your previous dashboard remains available.",
				);
		} finally {
			setPending(false);
		}
	};
	return (
		<div className="flex items-center justify-end gap-2">
			<Button variant="outline" size="sm" onClick={onOpen}>
				Open
			</Button>
			<DropdownMenu>
				<DropdownMenuTrigger
					render={
						<Button
							variant="ghost"
							size="icon"
							disabled={pending}
							aria-label={`More actions for "${record.name}"`}
						/>
					}
				>
					<MoreHorizontal className="size-4" />
				</DropdownMenuTrigger>
				<DropdownMenuContent align="end">
					<DropdownMenuItem
						onClick={() => {
							setName(record.name);
							setFailure(undefined);
							setRenaming(true);
						}}
					>
						Rename
					</DropdownMenuItem>
					<DropdownMenuItem onClick={() => void run(onDuplicate)}>
						Duplicate
					</DropdownMenuItem>
					{canShare && (
						<DropdownMenuItem onClick={() => void run(onShare)}>
							{record.visibility === "org"
								? "Make private"
								: "Share with organization"}
						</DropdownMenuItem>
					)}
					<DropdownMenuSeparator />
					<DropdownMenuItem
						variant="destructive"
						onClick={() => {
							setFailure(undefined);
							setDeleting(true);
						}}
					>
						Delete
					</DropdownMenuItem>
				</DropdownMenuContent>
			</DropdownMenu>
			<Dialog
				open={renaming}
				onOpenChange={(open) => {
					if (!pending) setRenaming(open);
				}}
			>
				<DialogContent>
					<DialogHeader>
						<DialogTitle>Rename dashboard</DialogTitle>
						<DialogDescription>
							Choose a name for {record.name}.
						</DialogDescription>
					</DialogHeader>
					<form
						className="space-y-4"
						onSubmit={(event) => {
							event.preventDefault();
							if (name.trim())
								void run(
									() => onRename(name.trim()),
									() => setRenaming(false),
								);
						}}
					>
						<Field label="Name">
							{(control) => (
								<Input
									{...control}
									value={name}
									disabled={pending}
									onChange={(event) => setName(event.target.value)}
								/>
							)}
						</Field>
						{failure && <ErrorState title={failure} />}
						<DialogFooter>
							<Button
								variant="outline"
								type="button"
								disabled={pending}
								onClick={() => setRenaming(false)}
							>
								Cancel
							</Button>
							<Button disabled={pending || !name.trim()} type="submit">
								{pending ? "Saving…" : "Save"}
							</Button>
						</DialogFooter>
					</form>
				</DialogContent>
			</Dialog>
			<AlertDialog
				open={deleting}
				onOpenChange={(open) => {
					if (!pending) setDeleting(open);
				}}
			>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete “{record.name}”?</AlertDialogTitle>
						<AlertDialogDescription>
							This removes the dashboard and its saved widgets. It can’t be
							undone.
						</AlertDialogDescription>
					</AlertDialogHeader>
					{failure && <ErrorState title={failure} />}
					<AlertDialogFooter>
						<AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
						<AlertDialogAction
							disabled={pending}
							onClick={(event) => {
								event.preventDefault();
								void run(onDelete, () => setDeleting(false));
							}}
						>
							{pending ? "Deleting…" : "Delete"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}
