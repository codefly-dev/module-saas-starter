"use client";

import { X } from "lucide-react";
import { type ReactNode, useRef, useState } from "react";
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
	ErrorState,
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
	const pending = useRef(false);
	return (
		<AlertDialog
			open={open}
			onOpenChange={(next) => {
				if (!pending.current) {
					setOpen(next);
					if (!next) revoke.reset();
				}
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
				{revoke.isError && (
					<ErrorState
						title="Couldn’t remove this role"
						detail={failureText("remove this role", revoke.error)}
					/>
				)}
				<AlertDialogFooter>
					<AlertDialogCancel disabled={revoke.isPending}>
						Cancel
					</AlertDialogCancel>
					<AlertDialogAction
						disabled={revoke.isPending}
						onClick={(event) => {
							event.preventDefault();
							if (pending.current) return;
							pending.current = true;
							revoke.mutate(
								{ subjectId, roleId, orgId, scope },
								{
									onSuccess: () => {
										pending.current = false;
										toast.success("Role removed");
										setOpen(false);
									},
									onError: () => {
										pending.current = false;
									},
								},
							);
						}}
					>
						{revoke.isPending ? "Removing…" : "Remove role"}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}
