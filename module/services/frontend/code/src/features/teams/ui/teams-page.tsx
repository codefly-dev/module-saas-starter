"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useCasting } from "@codefly-dev/saas-ui/casting";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Users } from "lucide-react";
import { useRouter } from "next/navigation";
import { useCallback, useMemo, useState } from "react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { useAuth } from "@/lib/auth";
import { failureText } from "@/shared/lib/failure-text";
import { readOutcome } from "@/shared/lib/read-outcome";
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
	ErrorState,
	Page as PageBody,
	PageHeader,
	Panel,
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
		error,
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
		onError: (error) => toast.error(failureText("create the team", error)),
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
		onError: (error) => toast.error(failureText("update the team", error)),
	});

	const deleteMutation = useMutation({
		mutationFn: (teamId: string) => teamMutations.remove(teamId),
		onSuccess: () => {
			toast.success("Team deleted");
			queryClient.invalidateQueries({ queryKey: ["teams", orgId] });
			setDeleteTarget(null);
		},
		onError: (error) => toast.error(failureText("delete the team", error)),
	});

	const handleViewMembers = useCallback(
		(team: Team) => {
			router.push(`/admin/teams/${encodeURIComponent(team.id)}`);
		},
		[router],
	);
	// What each team holds, for someone who may read it, and a view as it on
	// the row: the host decides each one when it is asked.
	const { ask, kept, teams: castable } = useCasting();
	const holdings = useMemo(
		() =>
			kept ? undefined : new Map(castable.map((team) => [team.id, team.holds])),
		[kept, castable],
	);
	const handleViewAs = useCallback(
		(team: Team) =>
			void ask({
				id: team.id,
				name: team.name,
				holds: holdings?.get(team.id) ?? [],
			}).then((refusal) =>
				refusal
					? toast.error(`Not viewing as ${team.name}: ${refusal.reason}`)
					: toast.success(`Viewing as ${team.name}`),
			),
		[ask, holdings],
	);
	// A refusal is not cured by asking again, so only a read that did not come
	// back offers the retry.
	const refused = readOutcome(isError, error) === "forbidden";
	const handleRename = useCallback((team: Team) => setRenameTarget(team), []);
	const handleDelete = useCallback((team: Team) => setDeleteTarget(team), []);

	return (
		<PageBody>
			<PageHeader
				title="Teams"
				description="Group the people of this organization, and choose what each group can use."
				actions={
					<>
						<OrgSelector />
						{orgId && canManage && (
							<Button onClick={() => setShowCreate(true)}>
								<Plus className="mr-2 h-4 w-4" />
								Create Team
							</Button>
						)}
					</>
				}
			/>

			{!orgId ? (
				<EmptyState
					icon={Users}
					title="Select an organization to view teams"
					description="Each team belongs to one organization. Pick an organization from the selector above."
				/>
			) : isError ? (
				<Panel>
					<Stack gap={3} align="start">
						<ErrorState
							title="Couldn't load the teams"
							detail={
								refused
									? "You don't have permission to see this organization's teams. Ask one of its administrators for access."
									: "Whether this organization has any is unknown. Try again; if it keeps failing, tell your administrator."
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
				<TeamsTable
					data={teams}
					isLoading={isLoading}
					canManage={canManage}
					onViewMembers={handleViewMembers}
					onRename={handleRename}
					onDelete={handleDelete}
					holdings={holdings && holdings.size > 0 ? holdings : undefined}
					onViewAs={handleViewAs}
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
		</PageBody>
	);
}
