"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useCallback, useState } from "react";
import { toast } from "sonner";
import { readOutcome } from "@/shared/lib/read-outcome";
import {
	Button,
	ErrorState,
	Page as PageBody,
	PageHeader,
	Panel,
	Stack,
} from "@/shared/ui";
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
	const {
		data: raw,
		isLoading,
		isError,
		error,
		refetch,
	} = useQuery(orgQueries.list());
	// A refusal is not cured by asking again, so only a read that did not come
	// back offers the retry.
	const refused = readOutcome(isError, error) === "forbidden";
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
		<PageBody>
			<PageHeader
				title="Organizations"
				description="The organizations you belong to, and who is in each."
				actions={
					// The deployment's creation policy decides; the server enforces it
					// either way, so this only stops offering what it would refuse.
					raw?.canCreate && !isError ? (
						<Button
							onClick={() => {
								createMutation.reset();
								setShowCreate(true);
							}}
						>
							<Plus className="mr-2 h-4 w-4" />
							Create organization
						</Button>
					) : undefined
				}
			/>

			{isError ? (
				<Panel>
					<Stack gap={3} align="start">
						<ErrorState
							title="Couldn't load the organizations"
							detail={
								refused
									? "You don't have permission to see the organizations. Ask an administrator for access."
									: "Whether you belong to any is unknown. Try again; if it keeps failing, tell your administrator."
							}
						/>
						{!refused && (
							<Button
								variant="outline"
								size="sm"
								onClick={() => void refetch()}
							>
								Try again
							</Button>
						)}
					</Stack>
				</Panel>
			) : (
				<OrganizationsTable
					data={orgs}
					isLoading={isLoading}
					onViewMembers={handleViewMembers}
				/>
			)}

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
				key={showCreate ? "create" : "closed"}
				error={
					createMutation.isError
						? lifecycleErrorMessage(
								createMutation.error,
								"Check your connection and try again.",
							)
						: undefined
				}
				open={showCreate}
				onSubmit={(vals) => createMutation.mutate(vals)}
				onCancel={() => setShowCreate(false)}
				isPending={createMutation.isPending}
			/>
		</PageBody>
	);
}
