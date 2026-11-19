"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { KeyRound } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { UserLabel } from "@/components/user-label";
import type { TriggerDelegationReference } from "@/gen/saas/accounts/v1/datasource_pb";
import { failureText } from "@/shared/lib/failure-text";
import { Badge, Button, ConfirmDialog, EmptyState, Section } from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { permissionsWords } from "../model/words";
import {
	useDelegateSourceTriggers,
	useRevokeTriggerDelegation,
} from "../service/mutations";

const column = createColumnHelper<TriggerDelegationReference>();

/**
 * Whose access a source's unattended work runs with. Nobody is signed in when
 * it runs, so it borrows one person's access to this source and can do only
 * what is listed. Stopping one takes effect from the next run; a run already
 * going is not interrupted, and reconnecting the source sets access up again.
 */
export function AccessSection({
	orgId,
	sourceId,
	sourceName,
	accesses,
	isLoading,
}: {
	orgId: string;
	sourceId: string;
	sourceName: string;
	accesses: TriggerDelegationReference[];
	isLoading: boolean;
}) {
	const setUp = useDelegateSourceTriggers(orgId);
	const stop = useRevokeTriggerDelegation(orgId);
	const [stopping, setStopping] = useState<TriggerDelegationReference | null>(
		null,
	);

	const columns = useMemo(
		() => [
			column.accessor("module", { header: "Module" }),
			column.accessor("ownerPrincipalId", {
				header: "Person",
				cell: (info) => (
					<UserLabel
						userId={info.getValue()}
						fallback="Someone no longer in this organization"
					/>
				),
			}),
			column.accessor("scopes", {
				header: "What it may do",
				cell: (info) => permissionsWords(info.getValue()),
			}),
			column.accessor("active", {
				header: "State",
				cell: (info) =>
					info.getValue() ? (
						<Badge tone="success" dot>
							In use
						</Badge>
					) : (
						<Badge tone="neutral">Stopped</Badge>
					),
			}),
			column.display({
				id: "actions",
				header: () => <span className="sr-only">Actions</span>,
				cell: ({ row }) =>
					row.original.active ? (
						<Button
							variant="outline"
							size="sm"
							onClick={() => setStopping(row.original)}
						>
							Stop using this access
						</Button>
					) : null,
			}),
		],
		[],
	);
	const table = useReactTable({
		data: accesses,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getRowId: (access) => access.delegationId,
	});

	const setUpButton = (
		<Button
			variant="outline"
			disabled={setUp.isPending}
			onClick={() =>
				setUp.mutate(sourceId, {
					onSuccess: (result) =>
						result.delegations.length > 0
							? toast.success(`Access set up for ${sourceName}`)
							: toast.info(
									"No installed module runs unattended work for this source, so there was nothing to set up.",
								),
					onError: (error) => toast.error(failureText("set up access", error)),
				})
			}
		>
			<KeyRound className="h-4 w-4" />
			{setUp.isPending ? "Setting up…" : "Set up access"}
		</Button>
	);

	const empty = !isLoading && accesses.length === 0;
	return (
		<Section
			title="Whose access it runs with"
			description="Nobody is signed in when this work runs, so it uses one person's access to this source and can do only what is listed here. Stopping takes effect from the next run."
			actions={empty ? undefined : setUpButton}
		>
			{empty ? (
				<EmptyState
					icon={<KeyRound />}
					heading="No one's access is set up for this source"
					description="Until someone's access is set up, unattended work on this source is refused."
				>
					{setUpButton}
				</EmptyState>
			) : (
				<DataTable
					table={table}
					isLoading={isLoading}
					emptyMessage="No one's access is set up for this source."
				/>
			)}

			{stopping && (
				<ConfirmDialog
					open
					onOpenChange={(open) => !open && setStopping(null)}
					title={
						<>
							Stop using{" "}
							<UserLabel
								userId={stopping.ownerPrincipalId}
								fallback="this person"
							/>
							&apos;s access for {sourceName}?
						</>
					}
					description={`Unattended work by ${stopping.module} on ${sourceName} will be refused from its next run. A run already going is not interrupted. Reconnecting the source restores this access.`}
					confirmLabel="Stop using this access"
					pendingLabel="Stopping…"
					isPending={stop.isPending}
					onConfirm={() =>
						stop.mutate(
							{ sourceId, delegationId: stopping.delegationId },
							{
								onSuccess: () =>
									toast.success(`Access stopped for ${sourceName}`),
								onError: (error) =>
									toast.error(failureText("stop using this access", error)),
								onSettled: () => setStopping(null),
							},
						)
					}
				/>
			)}
		</Section>
	);
}
