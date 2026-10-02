"use client";

import Link from "next/link";
import { toast } from "sonner";
import {
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/shared/ui";
import { lifecycleErrorMessage } from "../model/errors";
import { useLeaveOrganization } from "../service/lifecycle";

interface LeaveOrganizationDialogProps {
	organization: { id: string; name: string } | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}

export function LeaveOrganizationDialog({
	organization,
	open,
	onOpenChange,
}: LeaveOrganizationDialogProps) {
	const leave = useLeaveOrganization();
	// The server refuses the sole member and the last administrator with a
	// sentence that says what to do instead; it stays on the dialog, beside the
	// way to do it, rather than vanishing in a toast.
	const refusal = leave.isError
		? lifecycleErrorMessage(leave.error, "Couldn't leave the organization.")
		: null;

	const close = (next: boolean) => {
		if (!next) leave.reset();
		onOpenChange(next);
	};

	if (!organization) return null;
	return (
		<Dialog open={open} onOpenChange={close}>
			<DialogContent className="sm:max-w-md">
				<DialogHeader>
					<DialogTitle>Leave {organization.name}?</DialogTitle>
					<DialogDescription>
						You will lose access to {organization.name} and its teams. To come
						back, an administrator has to invite you again.
					</DialogDescription>
				</DialogHeader>
				{refusal && (
					<div role="alert" className="space-y-1 text-sm text-destructive">
						<p>{refusal}</p>
						<Link href="/admin/organizations" className="underline">
							Manage members
						</Link>
					</div>
				)}
				<DialogFooter>
					<Button variant="outline" onClick={() => close(false)}>
						Cancel
					</Button>
					<Button
						variant="destructive"
						disabled={leave.isPending}
						onClick={() =>
							leave.mutate(organization.id, {
								onSuccess: () => {
									toast.success(`You left ${organization.name}`);
									close(false);
								},
							})
						}
					>
						{leave.isPending ? "Leaving..." : "Leave organization"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
