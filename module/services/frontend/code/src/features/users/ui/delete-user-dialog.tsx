"use client";

import {
	AlertDialog,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	Button,
	ErrorState,
} from "@/shared/ui";

interface DeleteUserDialogProps {
	open: boolean;
	userEmail: string;
	onConfirm: () => void;
	onCancel: () => void;
	isPending: boolean;
	error?: string;
}

/** Confirm a soft-delete. Deleting also revokes the user's sessions server-side,
 * so it's a destructive, session-ending action — hence the explicit confirm. */
export function DeleteUserDialog({
	open,
	userEmail,
	onConfirm,
	onCancel,
	isPending,
	error,
}: DeleteUserDialogProps) {
	return (
		<AlertDialog
			open={open}
			onOpenChange={(o) => !o && !isPending && onCancel()}
		>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>Delete {userEmail}?</AlertDialogTitle>
					<AlertDialogDescription>
						This removes <span className="font-medium">{userEmail}</span> and
						revokes all their active sessions. They will be signed out
						everywhere and lose access.
					</AlertDialogDescription>
				</AlertDialogHeader>
				{error && <ErrorState title="User wasn’t deleted" detail={error} />}
				<AlertDialogFooter>
					<AlertDialogCancel disabled={isPending} onClick={onCancel}>
						Cancel
					</AlertDialogCancel>
					<Button
						onClick={() => {
							if (!isPending) onConfirm();
						}}
						disabled={isPending}
						variant="destructive"
					>
						{isPending ? "Deleting..." : "Delete user"}
					</Button>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}
