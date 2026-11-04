"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Users } from "lucide-react";
import { useRouter } from "next/navigation";
import { useCallback, useState } from "react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { useAuth } from "@/lib/auth";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	Button,
	Page,
	PageHeader,
	Stack,
} from "@/shared/ui";
import type { Team } from "../model/types";
import { teamMutations } from "../service/mutations";
import { teamQueries } from "../service/queries";
import { TeamForm } from "./team-form";
import { TeamsTable } from "./teams-table";

export function TeamsPage() {
	const { organizationId: orgId = "" } = useAuth();
	return <TeamsPageForOrganization key={orgId} orgId={orgId} />;
}

function TeamsPageForOrganization({ orgId }: { orgId: string }) {
	const router = useRouter();
	const { orgRole, platformRole } = useAuth();
	const canManage =
		orgRole === "owner" ||
		orgRole === "admin" ||
		platformRole === "super_admin";
	const queryClient = useQueryClient();
	const [showCreate, setShowCreate] = useState(false);
	const [renameTarget, setRenameTarget] = useState<Team | null>(null);
	const [deleteTarget, setDeleteTarget] = useState<Team | null>(null);

	// --- queries ---
	const {
		data: raw,
		isLoading,
		isError,
		refetch,
	} = useQuery({
		...teamQueries.list(orgId),
		enabled: !!orgId,
	});
	const teams: Team[] = (raw?.teams ?? []).map((t) => ({
		id: t.id,
		orgId: t.orgId,
		name: t.name,
		description: t.description,
		createdAt: t.createdAt
			? timestampDate(t.createdAt).toISOString()
			: undefined,
	}));

	// --- mutations ---
	const createMutation = useMutation({
		mutationFn: ({
			name,
			description,
		}: {
			name: string;
			description?: string;
		}) => teamMutations.create(orgId, name, description),
		onSuccess: () => {
			toast.success("Team created");
			queryClient.invalidateQueries({ queryKey: ["teams", orgId] });
			setShowCreate(false);
		},
		onError: () => toast.error("Failed to create team"),
	});

	const updateMutation = useMutation({
		mutationFn: ({
			teamId,
			name,
			description,
		}: {
			teamId: string;
			name: string;
			description?: string;
		}) => teamMutations.update(teamId, name, description),
		onSuccess: () => {
			toast.success("Team updated");
			queryClient.invalidateQueries({ queryKey: ["teams", orgId] });
			setRenameTarget(null);
		},
		onError: () => toast.error("Failed to update team"),
	});

	const deleteMutation = useMutation({
		mutationFn: (teamId: string) => teamMutations.remove(teamId),
		onSuccess: () => {
			toast.success("Team deleted");
			queryClient.invalidateQueries({ queryKey: ["teams", orgId] });
			setDeleteTarget(null);
		},
		onError: () => toast.error("Failed to delete team"),
	});

	const handleViewMembers = useCallback(
		(team: Team) => {
			router.push(`/admin/teams/${encodeURIComponent(team.id)}`);
		},
		[router],
	);
	const handleRename = useCallback((team: Team) => setRenameTarget(team), []);
	const handleDelete = useCallback((team: Team) => setDeleteTarget(team), []);

	return (
		<Page>
			<PageHeader
				title="Teams"
				description="A team groups people so a role granted once reaches all of them."
				actions={
					<Stack direction="row" gap={3} align="center">
						<OrgSelector />
						{orgId && canManage && (
							<Button onClick={() => setShowCreate(true)}>
								<Plus />
								Create team
							</Button>
						)}
					</Stack>
				}
			/>

			{!orgId ? (
				<EmptyState
					icon={Users}
					title="No organization selected"
					description="Pick an organization above to see the teams in it."
				/>
			) : isError ? (
				<div role="alert">
					Couldn&apos;t load teams.{" "}
					<Button variant="outline" onClick={() => void refetch()}>
						Retry
					</Button>
				</div>
			) : (
				<TeamsTable
					data={teams}
					isLoading={isLoading}
					canManage={canManage}
					onViewMembers={handleViewMembers}
					onRename={handleRename}
					onDelete={handleDelete}
				/>
			)}

			<TeamForm
				open={showCreate}
				onSubmit={(vals) => createMutation.mutate(vals)}
				onCancel={() => setShowCreate(false)}
				isPending={createMutation.isPending}
			/>

			{renameTarget && (
				<TeamForm
					key={renameTarget.id}
					open
					mode="edit"
					initial={{
						name: renameTarget.name,
						description: renameTarget.description,
					}}
					onSubmit={(vals) =>
						updateMutation.mutate({
							teamId: renameTarget.id,
							name: vals.name,
							description: vals.description,
						})
					}
					onCancel={() => setRenameTarget(null)}
					isPending={updateMutation.isPending}
				/>
			)}

			{deleteTarget && (
				<AlertDialog open onOpenChange={(o) => !o && setDeleteTarget(null)}>
					<AlertDialogContent>
						<AlertDialogHeader>
							<AlertDialogTitle>Delete team?</AlertDialogTitle>
							<AlertDialogDescription>
								This permanently deletes{" "}
								<span className="font-medium">{deleteTarget.name}</span> and
								removes all its memberships. This can&apos;t be undone.
							</AlertDialogDescription>
						</AlertDialogHeader>
						<AlertDialogFooter>
							<AlertDialogCancel onClick={() => setDeleteTarget(null)}>
								Cancel
							</AlertDialogCancel>
							<AlertDialogAction
								onClick={() => deleteMutation.mutate(deleteTarget.id)}
								disabled={deleteMutation.isPending}
								className="bg-destructive text-white hover:bg-destructive/90"
							>
								{deleteMutation.isPending ? "Deleting..." : "Delete team"}
							</AlertDialogAction>
						</AlertDialogFooter>
					</AlertDialogContent>
				</AlertDialog>
			)}
		</Page>
	);
}
