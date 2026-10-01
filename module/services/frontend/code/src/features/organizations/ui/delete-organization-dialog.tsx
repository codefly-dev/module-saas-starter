"use client";

import { useState } from "react";
import { toast } from "sonner";
import {
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	Input,
	Label,
} from "@/shared/ui";
import { lifecycleErrorMessage } from "../model/errors";
import { useDeleteOrganization } from "../service/lifecycle";

interface DeleteOrganizationDialogProps {
	organization: { id: string; name: string; slug: string } | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	onDeleted?: () => void;
}

export function DeleteOrganizationDialog({
	organization,
	open,
	onOpenChange,
	onDeleted,
}: DeleteOrganizationDialogProps) {
	const [typed, setTyped] = useState("");
	const remove = useDeleteOrganization();
	const refusal = remove.isError
		? lifecycleErrorMessage(remove.error, "Couldn't delete the organization.")
		: null;

	const close = (next: boolean) => {
		if (!next) {
			setTyped("");
			remove.reset();
		}
		onOpenChange(next);
	};

	if (!organization) return null;
	const confirmed =
		typed.trim().toLowerCase() === organization.slug.toLowerCase();
	return (
		<Dialog open={open} onOpenChange={close}>
			<DialogContent className="sm:max-w-lg">
				<DialogHeader>
					<DialogTitle>Delete {organization.name}?</DialogTitle>
					<DialogDescription>
						This cannot be undone from the product.
					</DialogDescription>
				</DialogHeader>
				<div className="space-y-3 text-sm">
					<p>Deleting the organization:</p>
					<ul className="list-disc space-y-1 pl-5">
						<li>removes every member, and signs them out of it;</li>
						<li>revokes its API keys and pending invitations;</li>
						<li>
							uninstalls its solutions and revokes data-source delegations.
						</li>
					</ul>
					<p className="text-muted-foreground">
						Its history — the audit trail and approval decisions — is kept.
					</p>
					<div className="space-y-2">
						<Label htmlFor="delete-org-confirm">
							Type <span className="font-mono">{organization.slug}</span> to
							confirm
						</Label>
						<Input
							id="delete-org-confirm"
							autoComplete="off"
							value={typed}
							onChange={(event) => setTyped(event.target.value)}
						/>
					</div>
					{refusal && (
						<p role="alert" className="text-destructive">
							{refusal}
						</p>
					)}
				</div>
				<DialogFooter>
					<Button variant="outline" onClick={() => close(false)}>
						Cancel
					</Button>
					<Button
						variant="destructive"
						disabled={!confirmed || remove.isPending}
						onClick={() =>
							remove.mutate(
								{ orgId: organization.id, confirmSlug: typed.trim() },
								{
									onSuccess: () => {
										toast.success(`${organization.name} was deleted`);
										close(false);
										onDeleted?.();
									},
								},
							)
						}
					>
						{remove.isPending ? "Deleting..." : "Delete organization"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
