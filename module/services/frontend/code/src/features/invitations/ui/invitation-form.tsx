"use client";

import { Plus } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import {
	InvitationDeliveryStatus,
	InvitationRole,
} from "@/gen/saas/accounts/v1/invitations_pb";
import {
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
	Input,
	Label,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import { useCreateInvitation } from "../service/mutations";
import { InvitationLinkDialog } from "./invitation-link-dialog";

export function InvitationForm({ orgId }: { orgId: string }) {
	const [open, setOpen] = useState(false);
	const [email, setEmail] = useState("");
	const [role, setRole] = useState(InvitationRole.MEMBER);
	const [link, setLink] = useState<{ url: string; email: string } | null>(null);

	const createInvitation = useCreateInvitation();

	function reset() {
		setEmail("");
		setRole(InvitationRole.MEMBER);
	}

	function handleSubmit() {
		if (!email.trim() || !orgId) return;
		createInvitation.mutate(
			{ orgId, email: email.trim(), role },
			{
				onSuccess: (response) => {
					const invited = email.trim();
					const emailed =
						response.invitation?.deliveryStatus ===
						InvitationDeliveryStatus.QUEUED;
					toast.success(
						emailed
							? `Invitation sent to ${invited}`
							: `Invitation created for ${invited}`,
					);
					reset();
					setOpen(false);
					// The link comes back whether or not it was also emailed, so the
					// invitation never depends on delivery.
					if (response.acceptUrl) {
						setLink({ url: response.acceptUrl, email: invited });
					}
				},
				onError: () => toast.error("Failed to create invitation"),
			},
		);
	}

	return (
		<>
			<Dialog open={open} onOpenChange={setOpen}>
				<DialogTrigger render={<Button disabled={!orgId} />}>
					<Plus className="mr-2 h-4 w-4" />
					Invite
				</DialogTrigger>
				<DialogContent className="sm:max-w-md">
					<DialogHeader>
						<DialogTitle>Invite Member</DialogTitle>
						<DialogDescription>
							Send an invitation to join the organization.
						</DialogDescription>
					</DialogHeader>

					<div className="space-y-4 py-4">
						<div className="space-y-2">
							<Label htmlFor="inv-email">Email</Label>
							<Input
								id="inv-email"
								type="email"
								placeholder="user@example.com"
								value={email}
								onChange={(e) => setEmail(e.target.value)}
							/>
						</div>

						<div className="space-y-2">
							<Label htmlFor="inv-role">Role</Label>
							<Select
								items={{
									[String(InvitationRole.MEMBER)]: "Member",
									[String(InvitationRole.ADMIN)]: "Admin",
								}}
								value={String(role)}
								onValueChange={(v) => {
									if (v) setRole(Number(v) as InvitationRole);
								}}
							>
								<SelectTrigger id="inv-role">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value={String(InvitationRole.MEMBER)}>
										Member
									</SelectItem>
									<SelectItem value={String(InvitationRole.ADMIN)}>
										Admin
									</SelectItem>
								</SelectContent>
							</Select>
						</div>
					</div>

					<DialogFooter>
						<Button
							variant="outline"
							onClick={() => {
								reset();
								setOpen(false);
							}}
						>
							Cancel
						</Button>
						<Button
							onClick={handleSubmit}
							disabled={createInvitation.isPending || !email.trim()}
						>
							{createInvitation.isPending ? "Sending..." : "Send Invite"}
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
			<InvitationLinkDialog
				url={link?.url ?? null}
				email={link?.email ?? ""}
				onClose={() => setLink(null)}
			/>
		</>
	);
}
