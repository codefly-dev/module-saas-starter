"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { useCallback, useState } from "react";
import { toast } from "sonner";
import { useAuth } from "@/lib/auth";
import { Button, Input } from "@/shared/ui";
import { toUserStatus, type User } from "../model/types";
import { type UserEdit, userMutations } from "../service/mutations";
import { userQueries } from "../service/queries";
import { DeleteUserDialog } from "./delete-user-dialog";
import { EditUserForm } from "./edit-user-form";
import { SuspendForm } from "./suspend-form";
import { UsersTable } from "./users-table";

export function UsersPage() {
	const queryClient = useQueryClient();
	const { enterImpersonation } = useAuth();
	const [search, setSearch] = useState("");
	const [suspendTarget, setSuspendTarget] = useState<User | null>(null);
	const [editTarget, setEditTarget] = useState<User | null>(null);
	const [deleteTarget, setDeleteTarget] = useState<User | null>(null);

	// --- queries ---
	const {
		data: raw,
		isLoading,
		isError,
		refetch,
	} = useQuery(userQueries.list(search));
	const users: User[] = (raw?.users ?? []).map((u) => ({
		uuid: u.uuid,
		primaryEmail: u.primaryEmail,
		createdAt: u.createdAt
			? timestampDate(u.createdAt).toISOString()
			: undefined,
		updatedAt: u.updatedAt
			? timestampDate(u.updatedAt).toISOString()
			: undefined,
		lastLogin: u.lastLogin
			? timestampDate(u.lastLogin).toISOString()
			: undefined,
		status: toUserStatus(u.status as unknown as number),
		profile: u.profile ?? {},
		emailVerified: u.emailVerified,
	}));

	// --- mutations ---
	const suspendMutation = useMutation({
		mutationFn: ({ userId, reason }: { userId: string; reason: string }) =>
			userMutations.suspend(userId, reason),
		onSuccess: () => {
			toast.success("User suspended");
			queryClient.invalidateQueries({ queryKey: ["users"] });
			setSuspendTarget(null);
		},
		onError: () => toast.error("Failed to suspend user"),
	});

	const unsuspendMutation = useMutation({
		mutationFn: (userId: string) => userMutations.unsuspend(userId),
		onSuccess: () => {
			toast.success("User unsuspended");
			queryClient.invalidateQueries({ queryKey: ["users"] });
		},
		onError: () => toast.error("Failed to unsuspend user"),
	});

	// Entering the session is the whole operation: the token never needs to be
	// shown, and the banner then owns the way back out.
	const impersonateMutation = useMutation({
		mutationFn: (userId: string) => userMutations.impersonate(userId),
		onSuccess: (data) => {
			const token = (data as Record<string, unknown>).accessToken as
				| string
				| undefined;
			if (!token) {
				toast.error("Impersonation returned no session");
				return;
			}
			enterImpersonation(token);
			toast.success("Impersonation session started");
		},
		onError: () => toast.error("Failed to impersonate user"),
	});

	const updateMutation = useMutation({
		mutationFn: (edit: UserEdit) => userMutations.update(edit),
		onSuccess: () => {
			toast.success("User updated");
			queryClient.invalidateQueries({ queryKey: ["users"] });
			setEditTarget(null);
		},
		onError: () => toast.error("Failed to update user"),
	});

	const deleteMutation = useMutation({
		mutationFn: (userId: string) => userMutations.remove(userId),
		onSuccess: () => {
			toast.success("User deleted");
			queryClient.invalidateQueries({ queryKey: ["users"] });
			setDeleteTarget(null);
		},
		onError: () => toast.error("Failed to delete user"),
	});

	const handleEdit = useCallback((user: User) => setEditTarget(user), []);
	const handleDelete = useCallback((user: User) => setDeleteTarget(user), []);
	const handleSuspend = useCallback((user: User) => setSuspendTarget(user), []);
	const handleUnsuspend = useCallback(
		(user: User) => unsuspendMutation.mutate(user.uuid),
		[unsuspendMutation],
	);
	const handleImpersonate = useCallback(
		(user: User) => impersonateMutation.mutate(user.uuid),
		[impersonateMutation],
	);

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<h2 className="text-2xl font-bold tracking-tight">Users</h2>
				<div className="relative w-80">
					<Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
					<Input
						placeholder="Search by email or name..."
						value={search}
						onChange={(e) => setSearch(e.target.value)}
						className="pl-9"
					/>
				</div>
			</div>

			{isError ? (
				<div
					role="alert"
					className="flex items-center justify-between gap-4 rounded-lg border border-destructive/30 bg-destructive/5 p-4"
				>
					<div>
						<p className="font-medium text-destructive">
							Users could not be loaded
						</p>
						<p className="text-sm text-muted-foreground">
							The directory request failed or your platform role is not active.
						</p>
					</div>
					<Button variant="outline" onClick={() => void refetch()}>
						Retry
					</Button>
				</div>
			) : (
				<UsersTable
					data={users}
					isLoading={isLoading}
					onEdit={handleEdit}
					onDelete={handleDelete}
					onSuspend={handleSuspend}
					onUnsuspend={handleUnsuspend}
					onImpersonate={handleImpersonate}
				/>
			)}

			{editTarget && (
				<EditUserForm
					open
					user={editTarget}
					onSubmit={(edit) => updateMutation.mutate(edit)}
					onCancel={() => setEditTarget(null)}
					isPending={updateMutation.isPending}
				/>
			)}

			{deleteTarget && (
				<DeleteUserDialog
					open
					userEmail={deleteTarget.primaryEmail}
					onConfirm={() => deleteMutation.mutate(deleteTarget.uuid)}
					onCancel={() => setDeleteTarget(null)}
					isPending={deleteMutation.isPending}
				/>
			)}

			{suspendTarget && (
				<SuspendForm
					open
					userId={suspendTarget.uuid}
					userEmail={suspendTarget.primaryEmail}
					onSubmit={(vals) => suspendMutation.mutate(vals)}
					onCancel={() => setSuspendTarget(null)}
					isPending={suspendMutation.isPending}
				/>
			)}
		</div>
	);
}
