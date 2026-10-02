"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	Building2,
	Check,
	ChevronsUpDown,
	LogOut,
	Plus,
	Settings,
} from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { SidebarMenuButton } from "@/components/ui/sidebar";
import { useAuth } from "@/lib/auth";
import { lifecycleErrorMessage } from "../model/errors";
import { orgMutations } from "../service/mutations";
import { orgQueries } from "../service/queries";
import { LeaveOrganizationDialog } from "./leave-organization-dialog";
import { OrgForm } from "./org-form";

/**
 * The organization the session is in, and every way out of it, in the product
 * shell: switch to another membership, create one when the deployment's policy
 * allows, leave, or open its settings. Shown with a single organization too —
 * the user should always be able to see where they are.
 */
export function OrgSwitcher() {
	const queryClient = useQueryClient();
	const { organizationId, switchOrganization } = useAuth();
	const { data, isLoading } = useQuery(orgQueries.list());
	const [switching, setSwitching] = useState(false);
	const [creating, setCreating] = useState(false);
	const [leaving, setLeaving] = useState(false);

	const organizations = data?.organizations ?? [];
	const current = organizations.find((org) => org.id === organizationId);

	const createMutation = useMutation({
		mutationFn: ({ name, slug }: { name: string; slug: string }) =>
			orgMutations.create(name, slug),
		onSuccess: async (response) => {
			toast.success("Organization created");
			setCreating(false);
			await queryClient.invalidateQueries({ queryKey: ["organizations"] });
			const created = response.organization?.id;
			if (created) await switchTo(created);
		},
		onError: (error) =>
			toast.error(
				lifecycleErrorMessage(error, "Couldn't create the organization"),
			),
	});

	async function switchTo(id: string) {
		if (id === organizationId || switching) return;
		setSwitching(true);
		try {
			await switchOrganization(id);
		} catch (error) {
			toast.error("Couldn't switch organization", {
				description: error instanceof Error ? error.message : "Try again.",
			});
		} finally {
			setSwitching(false);
		}
	}

	const label = isLoading
		? "Loading…"
		: switching
			? "Switching…"
			: (current?.name ?? "No organization");

	return (
		<>
			<DropdownMenu>
				<DropdownMenuTrigger
					render={<SidebarMenuButton aria-label="Organization" />}
				>
					<Building2 className="h-4 w-4" />
					<span className="truncate">{label}</span>
					<ChevronsUpDown className="ml-auto h-4 w-4" />
				</DropdownMenuTrigger>
				<DropdownMenuContent align="start" className="w-64">
					<DropdownMenuGroup>
						<DropdownMenuLabel>Organizations</DropdownMenuLabel>
						{organizations.map((org) => (
							<DropdownMenuItem
								key={org.id}
								disabled={switching}
								onClick={() => void switchTo(org.id)}
							>
								<span className="truncate">{org.name}</span>
								{org.id === organizationId && (
									<Check className="ml-auto h-4 w-4" aria-label="Current" />
								)}
							</DropdownMenuItem>
						))}
					</DropdownMenuGroup>
					<DropdownMenuSeparator />
					{data?.canCreate && (
						<DropdownMenuItem onClick={() => setCreating(true)}>
							<Plus className="mr-2 h-4 w-4" />
							Create organization
						</DropdownMenuItem>
					)}
					{current && (
						<>
							<DropdownMenuItem
								render={<Link href="/admin/organizations/settings" />}
							>
								<Settings className="mr-2 h-4 w-4" />
								Organization settings
							</DropdownMenuItem>
							<DropdownMenuItem onClick={() => setLeaving(true)}>
								<LogOut className="mr-2 h-4 w-4" />
								Leave {current.name}
							</DropdownMenuItem>
						</>
					)}
				</DropdownMenuContent>
			</DropdownMenu>
			<OrgForm
				open={creating}
				onSubmit={(values) => createMutation.mutate(values)}
				onCancel={() => setCreating(false)}
				isPending={createMutation.isPending}
			/>
			<LeaveOrganizationDialog
				organization={current ? { id: current.id, name: current.name } : null}
				open={leaving}
				onOpenChange={setLeaving}
			/>
		</>
	);
}
