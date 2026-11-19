"use client";

import { useRef, useState } from "react";
import { toast } from "sonner";
import {
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	ErrorState,
	Field,
	Input,
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
	const pending = useRef(false);
	const refusal = remove.isError
		? lifecycleErrorMessage(remove.error, "Couldn't delete the organization.")
		: null;

	const close = (next: boolean) => {
		if (pending.current) return;
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
					<Field
						label={
							<>
								Type <span className="font-mono">{organization.slug}</span> to
								confirm
							</>
						}
					>
						{(control) => (
							<Input
								{...control}
								autoComplete="off"
								value={typed}
								disabled={remove.isPending}
								onChange={(event) => setTyped(event.target.value)}
							/>
						)}
					</Field>
					{refusal && (
						<ErrorState
							title="Couldn’t delete the organization"
							detail={refusal}
						/>
					)}
				</div>
				<DialogFooter>
					<Button
						variant="outline"
						disabled={remove.isPending}
						onClick={() => close(false)}
					>
						Cancel
					</Button>
					<Button
						variant="destructive"
						disabled={!confirmed || remove.isPending}
						onClick={() => {
							if (!confirmed || pending.current) return;
							pending.current = true;
							remove.mutate(
								{ orgId: organization.id, confirmSlug: typed.trim() },
								{
									onError: () => {
										pending.current = false;
									},
									onSuccess: () => {
										pending.current = false;
										toast.success(`${organization.name} was deleted`);
										close(false);
										onDeleted?.();
									},
								},
							);
						}}
					>
						{remove.isPending ? "Deleting…" : "Delete organization"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
