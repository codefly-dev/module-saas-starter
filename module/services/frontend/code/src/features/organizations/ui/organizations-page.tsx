"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useCallback, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/shared/ui";
import { lifecycleErrorMessage } from "../model/errors";
import type { Organization } from "../model/types";
import { orgMutations } from "../service/mutations";
import { orgQueries } from "../service/queries";
import { OrgForm } from "./org-form";
import { OrganizationDetail } from "./organization-detail";
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
		onError: (error) =>
			toast.error(
				lifecycleErrorMessage(error, "Failed to create organization"),
			),
	});

	const handleViewMembers = useCallback((org: Organization) => {
		setSelectedOrg(org);
	}, []);

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<h2 data-slot="page-title" className="type-page-title">Organizations</h2>
				{/* The deployment's creation policy decides; the server enforces it
				    either way, so this only stops offering what it would refuse. */}
				{raw?.canCreate && (
					<Button onClick={() => setShowCreate(true)}>
						<Plus className="mr-2 h-4 w-4" />
						Create Organization
					</Button>
				)}
			</div>

			<OrganizationsTable
				data={orgs}
				isLoading={isLoading}
				onViewMembers={handleViewMembers}
			/>

			{selectedOrg && (
				<OrganizationDetail
					key={selectedOrg.id}
					organization={
						orgs.find((org) => org.id === selectedOrg.id) ?? selectedOrg
					}
					source="membership"
					onClose={() => setSelectedOrg(null)}
					onDeleted={() => setSelectedOrg(null)}
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
