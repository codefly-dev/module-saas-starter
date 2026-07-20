"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useCallback, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/shared/ui";
import type { Organization } from "../model/types";
import { orgMutations } from "../service/mutations";
import { orgQueries } from "../service/queries";
import { OrgForm } from "./org-form";
import { OrgMembersPanel } from "./org-members-panel";
import { OrganizationsTable } from "./organizations-table";

export function OrganizationsPage() {
	const queryClient = useQueryClient();
	const [showCreate, setShowCreate] = useState(false);
	const [selectedOrg, setSelectedOrg] = useState<Organization | null>(null);

	// --- queries ---
	const { data: raw, isLoading } = useQuery(orgQueries.list());
	const orgs: Organization[] = (raw?.organizations ?? []).map((o) => ({
		id: o.id,
		name: o.name,
		slug: o.slug,
		ownerId: o.ownerId,
		createdAt: o.createdAt
			? timestampDate(o.createdAt).toISOString()
			: undefined,
	}));

	// --- mutations ---
	const createMutation = useMutation({
		mutationFn: ({ name, slug }: { name: string; slug: string }) =>
			orgMutations.create(name, slug),
		onSuccess: () => {
			toast.success("Organization created");
			queryClient.invalidateQueries({ queryKey: ["organizations"] });
			setShowCreate(false);
		},
		onError: () => toast.error("Failed to create organization"),
	});

	const handleViewMembers = useCallback((org: Organization) => {
		setSelectedOrg(org);
	}, []);

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<h2 className="text-2xl font-bold tracking-tight">Organizations</h2>
				<Button onClick={() => setShowCreate(true)}>
					<Plus className="mr-2 h-4 w-4" />
					Create Organization
				</Button>
			</div>

			<OrganizationsTable
				data={orgs}
				isLoading={isLoading}
				onViewMembers={handleViewMembers}
			/>

			{selectedOrg && (
				<OrgMembersPanel
					orgId={selectedOrg.id}
					orgName={selectedOrg.name}
					onClose={() => setSelectedOrg(null)}
				/>
			)}

			<OrgForm
				open={showCreate}
				onSubmit={(vals) => createMutation.mutate(vals)}
				onCancel={() => setShowCreate(false)}
				isPending={createMutation.isPending}
			/>
		</div>
	);
}
