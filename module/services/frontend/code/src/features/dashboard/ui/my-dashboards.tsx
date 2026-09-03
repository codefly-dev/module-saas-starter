"use client";

import { ArrowLeft, MoreHorizontal, Plus } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { useAuth } from "@/lib/auth";
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
	DialogFooter,
	DialogHeader,
	DialogTitle,
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
	Input,
	Label,
} from "@/shared/ui";
import type { DashboardRecord } from "../model/record";
import {
	createBrowserDashboardLibrary,
	type DashboardLibrary,
	dashboardRecordStore,
} from "../service/dashboard-library";
import { createServerDashboardLibrary } from "../service/server-dashboard-library";
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
		? iso
		: parsed.toLocaleDateString(undefined, {
				year: "numeric",
				month: "short",
				day: "numeric",
			});
}

// MyDashboards is the user-owned dashboard surface: create, name, list, open,
// duplicate, rename, delete, and share the viewer's own dashboards, each a
// persisted record whose spec opens in the same <DashboardEditor> a single
// draft used. Persistence is the injected library's concern — localStorage
// today, a server store once one lands — so this surface is agnostic to where a
// dashboard lives, and to the authz that will gate a shared one.
export function MyDashboards({ library }: { library?: DashboardLibrary }) {
	const { user, organizationId } = useAuth();
	const scopedKey = scopedDashboardDraftKey(USER_DASHBOARD_LIBRARY_KEY, {
		organizationId,
		userId: user?.id,
	});
	const defaultLibrary = useMemo(
		() =>
			organizationId
				? createServerDashboardLibrary(organizationId)
				: createBrowserDashboardLibrary(scopedKey),
		[organizationId, scopedKey],
	);
	const activeLibrary = library ?? defaultLibrary;
	const { records, error, create, rename, setVisibility, duplicate, remove } =
		useDashboardLibrary(scopedKey, activeLibrary);

	const [openId, setOpenId] = useState<string | null>(null);
	const openRecord = records.find((record) => record.id === openId) ?? null;

	const recordStore = useMemo(
		() => (openId ? dashboardRecordStore(activeLibrary, openId) : null),
		[activeLibrary, openId],
	);

	if (openRecord && recordStore) {
		return (
			<div className="space-y-4">
				<div className="flex items-center gap-2">
					<Button
						type="button"
						variant="ghost"
						size="sm"
						onClick={() => setOpenId(null)}
					>
						<ArrowLeft className="mr-1 size-4" />
						All dashboards
					</Button>
					<span className="truncate font-medium">{openRecord.name}</span>
					<Badge variant="secondary" className="capitalize">
						{openRecord.visibility === "org" ? "Shared" : "Private"}
					</Badge>
				</div>
				<DashboardEditor
					storageKey={openRecord.id}
					initial={openRecord.spec}
					store={recordStore}
				/>
			</div>
		);
	}

	return (
		<div className="space-y-6">
			<CreateDashboardForm
				onCreate={async (name) => {
					// A failed create surfaces through `error`; swallow the rejection so
					// the fire-and-forget caller opens nothing rather than leaking it.
					try {
						const record = await create({
							name,
							spec: emptyDashboardSpec(name),
						});
						setOpenId(record.id);
					} catch {}
				}}
			/>

			{error && (
				<p className="text-sm text-destructive" role="alert">
					{error.message}
				</p>
			)}

			{records.length === 0 ? (
				<p className="text-sm text-muted-foreground">
					No dashboards yet. Name one above to start building against your live
					audit trail.
				</p>
			) : (
				<ul className="grid gap-3 sm:grid-cols-2">
					{records.map((record) => (
						<DashboardListItem
							key={record.id}
							record={record}
							onOpen={() => setOpenId(record.id)}
							onRename={(name) => rename(record.id, name)}
							onDuplicate={() => duplicate(record.id)}
							onShare={() =>
								setVisibility(
									record.id,
									record.visibility === "org" ? "private" : "org",
								)
							}
							onDelete={() => remove(record.id)}
						/>
					))}
				</ul>
			)}
		</div>
	);
}

function CreateDashboardForm({
	onCreate,
}: {
	onCreate: (name: string) => void;
}) {
	const [name, setName] = useState("");
	const submit = useCallback(() => {
		if (name.trim() === "") return;
		void onCreate(name.trim());
		setName("");
	}, [name, onCreate]);

	return (
		<Card>
			<CardHeader>
				<CardTitle>New dashboard</CardTitle>
			</CardHeader>
			<CardContent>
				<form
					className="flex flex-col gap-2 sm:flex-row sm:items-end"
					onSubmit={(event) => {
						event.preventDefault();
						submit();
					}}
				>
					<div className="flex-1 space-y-2">
						<Label htmlFor="new-dashboard-name">Name</Label>
						<Input
							id="new-dashboard-name"
							value={name}
							placeholder="Weekly activity"
							onChange={(event) => setName(event.target.value)}
						/>
					</div>
					<Button type="submit" disabled={name.trim() === ""}>
						<Plus className="mr-1 size-4" />
						Create
					</Button>
				</form>
			</CardContent>
		</Card>
	);
}

function DashboardListItem({
	record,
	onOpen,
	onRename,
	onDuplicate,
	onShare,
	onDelete,
}: {
	record: DashboardRecord;
	onOpen: () => void;
	onRename: (name: string) => void;
	onDuplicate: () => void;
	onShare: () => void;
	onDelete: () => void;
}) {
	const [renaming, setRenaming] = useState(false);
	const [confirmingDelete, setConfirmingDelete] = useState(false);
	const shared = record.visibility === "org";

	return (
		<li>
			<Card>
				<CardHeader className="flex flex-row items-start justify-between gap-2 space-y-0">
					<div className="min-w-0 space-y-1">
						<CardTitle className="truncate text-base">{record.name}</CardTitle>
						<p className="text-xs text-muted-foreground">
							Updated {formatUpdated(record.updatedAt)}
						</p>
					</div>
					<Badge variant={shared ? "default" : "secondary"}>
						{shared ? "Shared" : "Private"}
					</Badge>
				</CardHeader>
				<CardContent className="flex items-center justify-between gap-2">
					<Button type="button" variant="outline" size="sm" onClick={onOpen}>
						Open
					</Button>
					<DropdownMenu>
						<DropdownMenuTrigger
							render={
								<Button
									type="button"
									variant="ghost"
									size="icon"
									aria-label={`More actions for "${record.name}"`}
								/>
							}
						>
							<MoreHorizontal className="size-4" />
						</DropdownMenuTrigger>
						<DropdownMenuContent align="end">
							<DropdownMenuItem onClick={() => setRenaming(true)}>
								Rename
							</DropdownMenuItem>
							<DropdownMenuItem onClick={() => void onDuplicate()}>
								Duplicate
							</DropdownMenuItem>
							<DropdownMenuItem onClick={() => void onShare()}>
								{shared ? "Make private" : "Share with organization"}
							</DropdownMenuItem>
							<DropdownMenuSeparator />
							<DropdownMenuItem
								variant="destructive"
								onClick={() => setConfirmingDelete(true)}
							>
								Delete
							</DropdownMenuItem>
						</DropdownMenuContent>
					</DropdownMenu>
				</CardContent>
			</Card>

			<RenameDialog
				open={renaming}
				current={record.name}
				onOpenChange={setRenaming}
				onRename={onRename}
			/>

			<AlertDialog open={confirmingDelete} onOpenChange={setConfirmingDelete}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete “{record.name}”?</AlertDialogTitle>
						<AlertDialogDescription>
							This removes the dashboard and its saved widgets. It can’t be
							undone.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction onClick={() => void onDelete()}>
							Delete
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</li>
	);
}

function RenameDialog({
	open,
	current,
	onOpenChange,
	onRename,
}: {
	open: boolean;
	current: string;
	onOpenChange: (open: boolean) => void;
	onRename: (name: string) => void;
}) {
	const [name, setName] = useState(current);

	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				if (next) setName(current);
				onOpenChange(next);
			}}
		>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Rename dashboard</DialogTitle>
				</DialogHeader>
				<form
					className="space-y-4"
					onSubmit={(event) => {
						event.preventDefault();
						if (name.trim() === "") return;
						void onRename(name.trim());
						onOpenChange(false);
					}}
				>
					<div className="space-y-2">
						<Label htmlFor="rename-dashboard">Name</Label>
						<Input
							id="rename-dashboard"
							value={name}
							onChange={(event) => setName(event.target.value)}
						/>
					</div>
					<DialogFooter>
						<Button type="submit" disabled={name.trim() === ""}>
							Save
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
