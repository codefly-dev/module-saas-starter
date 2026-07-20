"use client";

import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/shared/ui";

interface DeleteUserDialogProps {
	open: boolean;
	userEmail: string;
	onConfirm: () => void;
	onCancel: () => void;
	isPending: boolean;
}

/** Confirm a soft-delete. Deleting also revokes the user's sessions server-side,
 * so it's a destructive, session-ending action — hence the explicit confirm. */
export function DeleteUserDialog({
	open,
	userEmail,
	onConfirm,
	onCancel,
	isPending,
}: DeleteUserDialogProps) {
	return (
		<AlertDialog open={open} onOpenChange={(o) => !o && onCancel()}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>Delete user?</AlertDialogTitle>
					<AlertDialogDescription>
						This soft-deletes <span className="font-medium">{userEmail}</span>{" "}
						and revokes all their active sessions. They will be signed out
						everywhere and lose access.
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel onClick={onCancel}>Cancel</AlertDialogCancel>
					<AlertDialogAction
						onClick={onConfirm}
						disabled={isPending}
						className="bg-destructive text-white hover:bg-destructive/90"
					>
						{isPending ? "Deleting..." : "Delete user"}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}
