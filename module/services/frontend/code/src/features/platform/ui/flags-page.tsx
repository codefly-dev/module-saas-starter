"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Plus } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import {
	Badge,
	Button,
	Checkbox,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
	Input,
	Label,
	Switch,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import type { FeatureFlag } from "../model/types";
import { useUpsertFeatureFlag } from "../service/mutations";
import { useFeatureFlags } from "../service/queries";

const col = createColumnHelper<FeatureFlag>();

export function FlagsPage() {
	const { data: flags = [], isLoading } = useFeatureFlags();
	const upsertFlag = useUpsertFeatureFlag();

	const [open, setOpen] = useState(false);
	const [name, setName] = useState("");
	const [description, setDescription] = useState("");
	const [enabled, setEnabled] = useState(true);
	const [rolloutPercent, setRolloutPercent] = useState(100);

	function reset() {
		setName("");
		setDescription("");
		setEnabled(true);
		setRolloutPercent(100);
	}

	function handleCreate() {
		if (!name.trim()) return;
		upsertFlag.mutate(
			{
				name: name.trim(),
				description: description.trim() || undefined,
				enabled,
				rolloutPercent,
			},
			{
				onSuccess: () => {
					toast.success(`Flag "${name.trim()}" created`);
					reset();
					setOpen(false);
				},
				onError: () => toast.error("Failed to create flag"),
			},
		);
	}

	const columns = useMemo(
		() => [
			col.accessor("name", { header: "Name" }),
			col.accessor("description", {
				header: "Description",
				cell: (info) => (
					<span className="text-muted-foreground">
						{info.getValue() || "-"}
					</span>
				),
			}),
			col.accessor("enabled", {
				header: "Status",
				cell: (info) => (
					<Badge variant={info.getValue() ? "default" : "secondary"}>
						{info.getValue() ? "Enabled" : "Disabled"}
					</Badge>
				),
			}),
			col.accessor("rolloutPercent", {
				header: "Rollout",
				cell: (info) => (
					<span className="text-muted-foreground">
						{info.getValue() != null ? `${info.getValue()}%` : "100%"}
					</span>
				),
			}),
			col.accessor("targetOrgIds", {
				header: "Target Orgs",
				cell: (info) => {
					const ids = info.getValue();
					if (!ids || ids.length === 0) {
						return <span className="text-muted-foreground">All</span>;
					}
					return (
						<Badge variant="outline" className="text-xs">
							{ids.length} orgs
						</Badge>
					);
				},
			}),
			col.display({
				id: "actions",
				header: "",
				cell: ({ row }) => {
					const flag = row.original;
					return (
						<Switch
							checked={flag.enabled}
							onCheckedChange={(checked) =>
								upsertFlag.mutate(
									{
										name: flag.name,
										description: flag.description || undefined,
										enabled: checked,
										rolloutPercent: flag.rolloutPercent,
										targetOrgIds: flag.targetOrgIds,
									},
									{
										onSuccess: () =>
											toast.success(
												`${flag.name} ${checked ? "enabled" : "disabled"}`,
											),
										onError: () => toast.error("Failed to update flag"),
									},
								)
							}
						/>
					);
				},
			}),
		],
		[upsertFlag],
	);

	const table = useReactTable({
		data: flags as FeatureFlag[],
		columns,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<div>
					<h1 className="text-2xl font-bold tracking-tight">Feature Flags</h1>
					<p className="text-muted-foreground">
						Create and toggle feature flags for gradual rollouts.
					</p>
				</div>

				<Dialog open={open} onOpenChange={setOpen}>
					<DialogTrigger render={<Button />}>
						<Plus className="mr-2 h-4 w-4" />
						Create Flag
					</DialogTrigger>
					<DialogContent className="sm:max-w-md">
						<DialogHeader>
							<DialogTitle>Create Feature Flag</DialogTitle>
							<DialogDescription>
								Define a new feature flag for gradual rollouts.
							</DialogDescription>
						</DialogHeader>
						<div className="space-y-4 py-4">
							<div className="space-y-2">
								<Label htmlFor="flag-name">Name</Label>
								<Input
									id="flag-name"
									placeholder="e.g. enable_new_dashboard"
									value={name}
									onChange={(e) => setName(e.target.value)}
								/>
							</div>
							<div className="space-y-2">
								<Label htmlFor="flag-desc">Description</Label>
								<Input
									id="flag-desc"
									placeholder="Optional description"
									value={description}
									onChange={(e) => setDescription(e.target.value)}
								/>
							</div>
							<div className="flex items-center gap-4">
								<div className="flex items-center gap-2">
									<Checkbox
										id="flag-enabled"
										checked={enabled}
										onCheckedChange={(v) => setEnabled(!!v)}
									/>
									<Label htmlFor="flag-enabled">Enabled</Label>
								</div>
								<div className="flex items-center gap-2">
									<Label htmlFor="flag-rollout">Rollout %</Label>
									<Input
										id="flag-rollout"
										type="number"
										min={0}
										max={100}
										value={rolloutPercent}
										onChange={(e) => setRolloutPercent(Number(e.target.value))}
										className="w-20"
									/>
								</div>
							</div>
						</div>
						<DialogFooter>
							<Button
								variant="outline"
								onClick={() => {
									reset();
									setOpen(false);
								}}
							>
								Cancel
							</Button>
							<Button
								onClick={handleCreate}
								disabled={upsertFlag.isPending || !name.trim()}
							>
								{upsertFlag.isPending ? "Creating..." : "Create Flag"}
							</Button>
						</DialogFooter>
					</DialogContent>
				</Dialog>
			</div>

			<DataTable
				table={table}
				isLoading={isLoading}
				emptyMessage="No feature flags"
			/>
		</div>
	);
}
