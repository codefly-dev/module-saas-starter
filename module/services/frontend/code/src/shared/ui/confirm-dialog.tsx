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
	ErrorState,
} from "@codefly-dev/ui/layout";
import type { ReactNode } from "react";

/**
 * The one question the product asks before an act that cannot be taken back.
 *
 * It is the kit's `AlertDialog` (focus trapped, Escape and the backdrop
 * cancel, the action is a kit `Button`) with the four things every such
 * question owes its reader fixed in one place: the title names the act and
 * the thing ("Delete the team Research?"), the description says what is lost,
 * the action button repeats the act instead of saying "OK", and the dialog
 * stays open and disabled while the act is in flight so a failure can be
 * read beside the question that caused it.
 *
 * The kit has no ready-made confirmation; this is a composition of its
 * primitives, not a second dialog.
 */
export function ConfirmDialog({
	open,
	onOpenChange,
	title,
	description,
	confirmLabel,
	pendingLabel,
	error,
	cancelLabel = "Cancel",
	destructive = true,
	isPending = false,
	onConfirm,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	/** The act and its object, as a question: "Delete the webhook …?" */
	title: ReactNode;
	/** What is lost, and whether it can be undone. */
	description: ReactNode;
	/** The act again, in sentence case: "Delete webhook". */
	confirmLabel: string;
	/** Shown on the action while it is in flight: "Deleting…". */
	pendingLabel?: string;
	error?: string;
	cancelLabel?: string;
	destructive?: boolean;
	isPending?: boolean;
	onConfirm: () => void;
}) {
	return (
		<AlertDialog
			open={open}
			onOpenChange={(next) => {
				// An act in flight is not cancelled by dismissing its question.
				if (!next && isPending) return;
				onOpenChange(next);
			}}
		>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{title}</AlertDialogTitle>
					<AlertDialogDescription>{description}</AlertDialogDescription>
				</AlertDialogHeader>
				{error && <ErrorState title="Not saved" detail={error} />}
				<AlertDialogFooter>
					<AlertDialogCancel disabled={isPending}>
						{cancelLabel}
					</AlertDialogCancel>
					<AlertDialogAction
						variant={destructive ? "destructive" : "default"}
						disabled={isPending}
						onClick={onConfirm}
					>
						{isPending && pendingLabel ? pendingLabel : confirmLabel}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}
