"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { OrgSelector } from "@/components/org-selector";
import type { Entitlement as EntitlementKey } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
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
	Input,
	Label,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import type { Entitlement } from "../model/types";
import { useOverrideEntitlement } from "../service/mutations";
import { useOrgEntitlements } from "../service/queries";

const col = createColumnHelper<Entitlement>();

function UsageBar({ limit, used }: { limit: number; used: number }) {
	if (limit <= 0) return null;
	const pct = Math.min(100, Math.round((used / limit) * 100));
	const color =
		pct > 80 ? "bg-destructive" : pct > 50 ? "bg-yellow-500" : "bg-green-500";
	return (
		<div className="flex items-center gap-2">
			<div className="flex-1 rounded-full bg-muted h-2">
				<div
					className={`h-2 rounded-full ${color}`}
					style={{ width: `${pct}%` }}
				/>
			</div>
			<span className="text-xs text-muted-foreground">{pct}%</span>
		</div>
	);
}

export function EntitlementsPage() {
	const { organizationId: orgId = "" } = useAuth();
	return <EntitlementsPageForOrganization key={orgId} orgId={orgId} />;
}

function EntitlementsPageForOrganization({ orgId }: { orgId: string }) {
	const { data, isLoading } = useOrgEntitlements(orgId || null);
	const overrideEntitlement = useOverrideEntitlement();

	const [overrideTarget, setOverrideTarget] = useState<EntitlementKey | null>(
		null,
	);
	const [limitValue, setLimitValue] = useState("");
	const [reason, setReason] = useState("");

	function handleOverride() {
		if (!orgId || !overrideTarget || !limitValue) return;
		overrideEntitlement.mutate(
			{
				orgId,
				feature: overrideTarget,
				limitValue: BigInt(limitValue),
				reason: reason.trim() || undefined,
			},
			{
				onSuccess: () => {
					toast.success("Entitlement override applied");
					setOverrideTarget(null);
					setLimitValue("");
					setReason("");
				},
				onError: () => toast.error("Failed to apply override"),
			},
		);
	}

	const columns = useMemo(
		() => [
			col.accessor("feature", { header: "Feature" }),
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
				header: "Override",
				cell: (info) =>
					info.getValue() ? <Badge variant="outline">Override</Badge> : null,
			}),
			col.display({
				id: "actions",
				header: "",
				cell: ({ row }) => (
					<Button
						variant="ghost"
						size="sm"
						onClick={() => {
							setOverrideTarget(row.original.feature);
							setLimitValue(String(Number(row.original.limit)));
						}}
					>
						Override
					</Button>
				),
			}),
		],
		[],
	);

	const table = useReactTable({
		data: (data?.entitlements ?? []) as Entitlement[],
		columns,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
	});

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<div className="flex items-center gap-4">
					<div>
						<h1 className="text-2xl font-bold tracking-tight">Entitlements</h1>
						<p className="text-muted-foreground">
							View and override organization entitlements.
						</p>
					</div>
					{data?.planName && <Badge variant="secondary">{data.planName}</Badge>}
				</div>
				<OrgSelector />
			</div>

			<DataTable
				table={table}
				isLoading={orgId ? isLoading : false}
				emptyMessage={orgId ? "No entitlements" : "Select an organization"}
			/>

			<Dialog
				open={!!overrideTarget}
				onOpenChange={(v) => {
					if (!v) {
						setOverrideTarget(null);
						setLimitValue("");
						setReason("");
					}
				}}
			>
				<DialogContent className="sm:max-w-md">
					<DialogHeader>
						<DialogTitle>Override Entitlement</DialogTitle>
						<DialogDescription>
							Override the limit for{" "}
							<span className="font-mono">{overrideTarget}</span>.
						</DialogDescription>
					</DialogHeader>
					<div className="space-y-4 py-4">
						<div className="space-y-2">
							<Label htmlFor="ent-limit">New Limit</Label>
							<Input
								id="ent-limit"
								type="number"
								placeholder="Limit value (-1 for unlimited)"
								value={limitValue}
								onChange={(e) => setLimitValue(e.target.value)}
							/>
						</div>
						<div className="space-y-2">
							<Label htmlFor="ent-reason">Reason</Label>
							<Input
								id="ent-reason"
								placeholder="Optional reason"
								value={reason}
								onChange={(e) => setReason(e.target.value)}
							/>
						</div>
					</div>
					<DialogFooter>
						<Button
							variant="outline"
							onClick={() => {
								setOverrideTarget(null);
								setLimitValue("");
								setReason("");
							}}
						>
							Cancel
						</Button>
						<Button
							onClick={handleOverride}
							disabled={overrideEntitlement.isPending || !limitValue}
						>
							{overrideEntitlement.isPending ? "Applying..." : "Apply Override"}
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		</div>
	);
}
