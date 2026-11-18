"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Ban } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { failureText } from "@/shared/lib/failure-text";
import { formatDate } from "@/shared/lib/utils";
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
	ErrorState,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { formatEnvironment, maskKeyPrefix } from "../model/transforms";
import type { APIKey } from "../model/types";
import { useRevokeAPIKey } from "../service/mutations";

const col = createColumnHelper<APIKey>();

export function APIKeysTable({
	keys,
	isLoading,
	canManage = false,
}: {
	keys: APIKey[];
	isLoading: boolean;
	canManage?: boolean;
}) {
	"use no memo";
	// TanStack Table mutates a stable table instance when its data changes.
	// Compiler-cached JSX would otherwise keep rendering the previous key list.
	const revokeKey = useRevokeAPIKey();
	const [selectedKey, setSelectedKey] = useState<APIKey | null>(null);
	const [failure, setFailure] = useState<string | null>(null);
	const submitting = useRef(false);

	const columns = useMemo(
		() => [
			col.accessor("name", { header: "Name" }),
			col.accessor("prefix", {
				header: "Key",
				cell: (info) => (
					<code className="rounded bg-muted px-2 py-1 text-xs font-mono">
						{maskKeyPrefix(info.getValue())}
					</code>
				),
			}),
			col.accessor("environment", {
				header: "Environment",
				cell: (info) => {
					const env = formatEnvironment(info.getValue());
					return (
						<Badge variant={env === "live" ? "default" : "secondary"}>
							{env}
						</Badge>
					);
				},
			}),
			col.accessor("lastUsedAt", {
				header: "Last used",
				cell: (info) => {
					const v = info.getValue();
					// "Never" for keys that have been issued but not used yet —
					// distinguishes from a load-time gap. formatDate("-") would
					// be ambiguous with an unknown timestamp.
					return (
						<span
							className={
								v ? "text-muted-foreground" : "italic text-muted-foreground/70"
							}
						>
							{v ? formatDate(v) : "Never"}
						</span>
					);
				},
			}),
			col.accessor("createdAt", {
				header: "Created",
				cell: (info) => (
					<span className="text-muted-foreground">
						{formatDate(info.getValue())}
					</span>
				),
			}),
			col.display({
				id: "actions",
				header: "",
				cell: ({ row }) => {
					const key = row.original;
					if (!canManage) return null;
					if (key.revokedAt) {
						return (
							<Badge variant="destructive" className="text-xs">
								Revoked
							</Badge>
						);
					}
					return (
						<Button
							variant="ghost"
							size="sm"
							aria-label={`Revoke ${key.name}`}
							onClick={() => {
								setFailure(null);
								setSelectedKey(key);
							}}
						>
							<Ban className="h-4 w-4 text-destructive" />
						</Button>
					);
				},
			}),
		],
		[canManage],
	);

	// Active keys first, revoked keys at the bottom — same end-state
	// a Stripe-style table reaches by default. Within each group the
	// backend's createdAt-desc ordering is preserved.
	const orderedKeys = useMemo(() => {
		const active = keys.filter((k) => !k.revokedAt);
		const revoked = keys.filter((k) => k.revokedAt);
		return [...active, ...revoked];
	}, [keys]);

	const table = useReactTable({
		data: orderedKeys,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});

	return (
		<>
			<DataTable
				table={table}
				isLoading={isLoading}
				ariaLabel="API keys"
				emptyMessage={
					canManage
						? "No API keys yet. Create a key to connect an application."
						: "No API keys yet. An organization administrator can create one."
				}
			/>
			<AlertDialog
				open={selectedKey !== null}
				onOpenChange={(open) => {
					if (!open && !submitting.current) {
						setSelectedKey(null);
						setFailure(null);
					}
				}}
			>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Revoke {selectedKey?.name}?</AlertDialogTitle>
						<AlertDialogDescription>
							This permanently revokes the key. Applications using it will lose
							access immediately.
						</AlertDialogDescription>
					</AlertDialogHeader>
					{failure && (
						<ErrorState title="Couldn't revoke the key" detail={failure} />
					)}
					<AlertDialogFooter>
						<AlertDialogCancel disabled={revokeKey.isPending}>
							Cancel
						</AlertDialogCancel>
						<AlertDialogAction
							disabled={revokeKey.isPending}
							onClick={(event) => {
								event.preventDefault();
								if (!selectedKey || !canManage || submitting.current) return;
								submitting.current = true;
								setFailure(null);
								revokeKey.mutate(
									{
										id: selectedKey.id,
										organizationId: selectedKey.organizationId,
									},
									{
										onSuccess: () => {
											toast.success(`Key "${selectedKey.name}" revoked`);
											setSelectedKey(null);
										},
										onError: (error) =>
											setFailure(failureText("revoke this key", error)),
										onSettled: () => {
											submitting.current = false;
										},
									},
								);
							}}
						>
							{revokeKey.isPending ? "Revoking…" : "Revoke"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</>
	);
}
