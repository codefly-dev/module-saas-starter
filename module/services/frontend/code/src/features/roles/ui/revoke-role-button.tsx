"use client";

import { X } from "lucide-react";
import { useState, type ReactNode } from "react";
import { toast } from "sonner";
import { failureText } from "@/shared/lib/failure-text";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogTrigger,
	Button,
} from "@/shared/ui";
import { useRevokeRole } from "../service/mutations";

/** The same confirmed removal from a person's page, a role, and the roles dialog. */
export function RevokeRoleButton({
	subjectId,
	subjectLabel,
	roleId,
	roleName,
	orgId,
	scope = "",
}: {
	subjectId: string;
	subjectLabel: ReactNode;
	roleId: string;
	roleName: string;
	orgId: string;
	scope?: string;
}) {
	const [open, setOpen] = useState(false);
	const revoke = useRevokeRole();
	return (
		<AlertDialog
			open={open}
			onOpenChange={(next) => {
				if (!revoke.isPending) setOpen(next);
			}}
		>
			<AlertDialogTrigger
				render={
					<Button
						variant="ghost"
						size="icon-xs"
						aria-label={`Remove ${roleName}`}
					/>
				}
			>
				<X aria-hidden />
			</AlertDialogTrigger>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>Remove {roleName}?</AlertDialogTitle>
					<AlertDialogDescription>
						Remove this role from {subjectLabel}
						{scope ? ` in ${scope}` : ""}? They will lose the access provided by
						this assignment. Access from other roles or teams will remain.
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel disabled={revoke.isPending}>
						Cancel
					</AlertDialogCancel>
					<AlertDialogAction
						disabled={revoke.isPending}
						onClick={() =>
							revoke.mutate(
								{ subjectId, roleId, orgId, scope },
								{
									onSuccess: () => {
										toast.success("Role removed");
										setOpen(false);
									},
									onError: (error) =>
										toast.error(failureText("remove this role", error)),
								},
							)
						}
					>
						{revoke.isPending ? "Removing…" : "Remove role"}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}
