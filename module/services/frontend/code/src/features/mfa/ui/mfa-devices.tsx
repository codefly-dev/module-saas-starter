"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Trash2 } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { failureText } from "@/shared/lib/failure-text";
import { formatDate } from "@/shared/lib/utils";
import {
	AlertDialog,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	Badge,
	Button,
	ErrorState,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { formatDeviceType } from "../model/transforms";
import type { MFADevice } from "../model/types";

const col = createColumnHelper<MFADevice>();

interface MFADevicesProps {
	data: MFADevice[];
	isLoading: boolean;
	onRevoke: (device: MFADevice) => Promise<unknown>;
}

export function MFADevices({ data, isLoading, onRevoke }: MFADevicesProps) {
	const [selected, setSelected] = useState<MFADevice>();
	const [pending, setPending] = useState(false);
	const [error, setError] = useState<string>();
	const busy = useRef(false);
	async function remove() {
		if (!selected || busy.current) return;
		busy.current = true;
		setPending(true);
		setError(undefined);
		try {
			await onRevoke(selected);
			setSelected(undefined);
		} catch (failure) {
			setError(failureText("remove the security method", failure));
		} finally {
			busy.current = false;
			setPending(false);
		}
	}
	const columns = useMemo(
		() => [
			col.accessor("name", {
				header: "Name",
				cell: (info) => <span className="font-medium">{info.getValue()}</span>,
			}),
			col.accessor("type", {
				header: "Type",
				cell: (info) => (
					<Badge variant="secondary">{formatDeviceType(info.getValue())}</Badge>
				),
			}),
			col.accessor("createdAt", {
				header: "Added",
				cell: (info) => (
					<span className="text-muted-foreground text-sm">
						{formatDate(info.getValue())}
					</span>
				),
			}),
			col.accessor("lastUsedAt", {
				header: "Last used",
				cell: (info) => (
					<span className="text-muted-foreground text-sm">
						{formatDate(info.getValue())}
					</span>
				),
			}),
			col.display({
				id: "actions",
				cell: ({ row }) => {
					const device = row.original;
					return (
						<Button
							variant="ghost"
							size="icon"
							aria-label={`Remove ${device.name}`}
							onClick={() => {
								setError(undefined);
								setSelected(device);
							}}
						>
							<Trash2 />
						</Button>
					);
				},
			}),
		],
		[],
	);

	const table = useReactTable({
		data,
		columns,
		getCoreRowModel: getCoreRowModel(),
	});

	return (
		<>
			<DataTable
				table={table}
				isLoading={isLoading}
				ariaLabel="Security methods"
				emptyMessage="No security methods yet. Add a passkey or authenticator app to protect your account."
			/>
			<AlertDialog
				open={!!selected}
				onOpenChange={(open) => {
					if (!open && !busy.current) setSelected(undefined);
				}}
			>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Remove {selected?.name}?</AlertDialogTitle>
						<AlertDialogDescription>
							This security method will no longer work for sign-in. You can add
							it again later.
						</AlertDialogDescription>
					</AlertDialogHeader>
					{error && (
						<ErrorState title="Security method wasn’t removed" detail={error} />
					)}
					<AlertDialogFooter>
						<AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
						<Button variant="destructive" disabled={pending} onClick={remove}>
							{pending ? "Removing…" : "Remove security method"}
						</Button>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</>
	);
}
