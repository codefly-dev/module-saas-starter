"use client";
import { Progress } from "@codefly-dev/ui/layout";

import {
	createColumnHelper,
	getCoreRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { OrgSelector } from "@/components/org-selector";
import { refusalMessage, refusalToast } from "@/features/auth/model/step-up";
import {
	ENTITLEMENT_DEFINITIONS,
	type Entitlement as EntitlementKey,
} from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import { readOutcome } from "@/shared/lib/read-outcome";
import { formatLimit } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	ErrorState,
	Field,
	Input,
	Page as PageBody,
	PageHeader,
	Panel,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { parseEntitlementLimit } from "../model/limit";
import type { Entitlement } from "../model/types";
import { useOverrideEntitlement } from "../service/mutations";
import { useOrgEntitlements } from "../service/queries";

const col = createColumnHelper<Entitlement>();

// The rows when there is no answer. TanStack Table resets the page on every new
// `data` array and that reset re-renders this page, so a fresh `[]` per render
// would re-render it forever while the read is refused or disabled.
const NO_ENTITLEMENTS: Entitlement[] = [];

function UsageBar({ limit, used }: { limit: number; used: number }) {
	if (limit <= 0) return null;
	const pct = Math.min(100, Math.round((used / limit) * 100));
	const tone = pct > 80 ? "danger" : pct > 50 ? "warning" : "success";
	return (
		<div className="flex items-center gap-2">
			<Progress
				value={pct}
				tone={tone}
				label="Plan usage"
				valueText={`${used.toLocaleString()} of ${limit.toLocaleString()} used`}
				className="flex-1"
			/>
			<span className="text-xs text-muted-foreground">{pct}%</span>
		</div>
	);
}

export function EntitlementsPage() {
	const { organizationId: orgId = "" } = useAuth();
	return <EntitlementsPageForOrganization key={orgId} orgId={orgId} />;
}

function EntitlementsPageForOrganization({ orgId }: { orgId: string }) {
	const { platformRole } = useAuth();
	const canManage = Boolean(platformRole);
	const busy = useRef(false);
	const {
		data,
		isLoading,
		isError,
		error: readError,
	} = useOrgEntitlements(orgId || null);
	// A failed read leaves no rows, exactly as an organization with no
	// entitlements does; the table is replaced so the two cannot be confused.
	const outcome = readOutcome(isError, readError);
	const overrideEntitlement = useOverrideEntitlement();

	const [overrideTarget, setOverrideTarget] = useState<EntitlementKey | null>(
		null,
	);
	const [limitValue, setLimitValue] = useState("");
	const [reason, setReason] = useState("");
	const [error, setError] = useState("");

	function handleOverride() {
		if (!canManage || busy.current || !orgId || !overrideTarget) return;
		const limit = parseEntitlementLimit(limitValue);
		if (limit === null) {
			setError(
				"Enter a whole number of zero or more, or -1 for unlimited. The value must fit the supported limit.",
			);
			return;
		}
		setError("");
		busy.current = true;
		overrideEntitlement.mutate(
			{
				orgId,
				feature: overrideTarget,
				limitValue: limit,
				reason: reason.trim() || undefined,
			},
			{
				onSuccess: () => {
					toast.success("Plan limit updated");
					setOverrideTarget(null);
					setLimitValue("");
					setReason("");
				},
				onError: (failure) =>
					refusalToast(failure, "Couldn't update the limit"),
				onSettled: () => {
					busy.current = false;
				},
			},
		);
	}

	const { reset: resetOverride } = overrideEntitlement;
	const columns = useMemo(
		() => [
			col.accessor("feature", {
				header: "Feature",
				cell: (info) => ENTITLEMENT_DEFINITIONS[info.getValue()].description,
			}),
			col.accessor("limit", {
				header: "Limit",
				cell: (info) => formatLimit(info.getValue()),
			}),
			col.accessor("used", {
				header: "Used",
				cell: (info) => Number(info.getValue()).toLocaleString(),
			}),
			col.display({
				id: "usage",
				header: "Usage",
				cell: ({ row }) => (
					<div className="w-32">
						<UsageBar
							limit={Number(row.original.limit)}
							used={Number(row.original.used)}
						/>
					</div>
				),
			}),
			col.accessor("hasOverride", {
				header: "Custom limit",
				cell: (info) =>
					info.getValue() ? (
						<Badge variant="outline">Custom limit</Badge>
					) : null,
			}),
			col.display({
				id: "actions",
				header: "",
				cell: ({ row }) =>
					canManage ? (
						<Button
							variant="ghost"
							size="sm"
							onClick={() => {
								resetOverride();
								setOverrideTarget(row.original.feature);
								setLimitValue(String(row.original.limit));
								setError("");
							}}
						>
							Change limit
						</Button>
					) : null,
			}),
		],
		[canManage, resetOverride],
	);

	const table = useReactTable({
		data: (data?.entitlements ?? NO_ENTITLEMENTS) as Entitlement[],
		columns,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
	});

	return (
		<PageBody>
			<PageHeader
				title="Plan limits"
				description="Review what this organization can use and its current limits."
				actions={<OrgSelector />}
			>
				{data?.planName && <Badge variant="secondary">{data.planName}</Badge>}
			</PageHeader>

			{isError ? (
				<Panel>
					<ErrorState
						title="Couldn't load the plan limits"
						detail={
							outcome === "forbidden"
								? "You don't have access to these plan limits. Ask a platform administrator to grant access."
								: "Reload the page to try again. If it keeps failing, tell your administrator."
						}
					/>
				</Panel>
			) : (
				<DataTable
					table={table}
					isLoading={orgId ? isLoading : false}
					emptyMessage={
						orgId
							? "No plan limits are configured. Ask a platform administrator to review this organization’s plan."
							: "Select an organization"
					}
				/>
			)}

			<Dialog
				open={!!overrideTarget}
				onOpenChange={(v) => {
					if (busy.current || overrideEntitlement.isPending) return;
					if (!v) {
						setOverrideTarget(null);
						setLimitValue("");
						setReason("");
					}
				}}
			>
				<DialogContent className="sm:max-w-md">
					<DialogHeader>
						<DialogTitle>Change plan limit</DialogTitle>
						<DialogDescription>
							{overrideTarget &&
								ENTITLEMENT_DEFINITIONS[overrideTarget].description}
						</DialogDescription>
					</DialogHeader>
					<form
						id="change-plan-limit"
						noValidate
						className="space-y-4 py-4"
						onSubmit={(event) => {
							event.preventDefault();
							handleOverride();
						}}
					>
						<Field
							label="New limit"
							description="Use -1 for unlimited."
							error={error || undefined}
						>
							{(control) => (
								<Input
									{...control}
									inputMode="numeric"
									value={limitValue}
									disabled={overrideEntitlement.isPending}
									onChange={(event) => setLimitValue(event.target.value)}
								/>
							)}
						</Field>
						<Field label="Reason (optional)">
							{(control) => (
								<Input
									{...control}
									value={reason}
									disabled={overrideEntitlement.isPending}
									onChange={(event) => setReason(event.target.value)}
								/>
							)}
						</Field>
						{overrideEntitlement.isError && (
							<ErrorState
								title="Limit wasn’t updated"
								detail={refusalMessage(
									overrideEntitlement.error,
									"Your entries have been kept. Try saving again.",
								)}
							/>
						)}
					</form>
					<DialogFooter>
						<Button
							variant="outline"
							disabled={overrideEntitlement.isPending}
							onClick={() => {
								setOverrideTarget(null);
								setLimitValue("");
								setReason("");
							}}
						>
							Cancel
						</Button>
						<Button
							type="submit"
							form="change-plan-limit"
							disabled={overrideEntitlement.isPending || !canManage}
						>
							{overrideEntitlement.isPending ? "Saving…" : "Save limit"}
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		</PageBody>
	);
}
