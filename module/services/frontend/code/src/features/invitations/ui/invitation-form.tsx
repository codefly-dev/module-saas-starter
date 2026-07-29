"use client";

import { Plus } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { InvitationRole } from "@/gen/saas/accounts/v1/invitations_pb";
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

export function InvitationForm({ orgId }: { orgId: string }) {
	const [open, setOpen] = useState(false);
	const [email, setEmail] = useState("");
	const [role, setRole] = useState(InvitationRole.MEMBER);

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
				onSuccess: () => {
					toast.success(`Invitation sent to ${email.trim()}`);
					reset();
					setOpen(false);
				},
				onError: () => toast.error("Failed to send invitation"),
			},
		);
	}

	return (
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
						<Label>Role</Label>
						<Select
							value={String(role)}
							onValueChange={(v) => {
								if (v) setRole(Number(v) as InvitationRole);
							}}
						>
							<SelectTrigger>
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
	);
}
