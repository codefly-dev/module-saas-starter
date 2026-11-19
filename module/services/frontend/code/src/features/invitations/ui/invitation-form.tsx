"use client";

import { Plus } from "lucide-react";
import { useId, useRef, useState } from "react";
import { toast } from "sonner";
import {
	isMFARequired,
	refusalMessage,
	stepUpToast,
} from "@/features/auth/model/step-up";
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
	ErrorState,
	Field,
	Input,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import { createInvitationSchema } from "../model/schemas";
import { useCreateInvitation } from "../service/mutations";
import { InvitationLinkDialog } from "./invitation-link-dialog";

export function InvitationForm({ orgId }: { orgId: string }) {
	const formId = useId();
	const [open, setOpen] = useState(false);
	const [email, setEmail] = useState("");
	const [role, setRole] = useState(InvitationRole.MEMBER);
	const [emailError, setEmailError] = useState<string>();
	const [failure, setFailure] = useState<string>();
	const [link, setLink] = useState<{ url: string; email: string } | null>(null);
	const submitting = useRef(false);
	const createInvitation = useCreateInvitation();
	function reset() {
		setEmail("");
		setRole(InvitationRole.MEMBER);
		setEmailError(undefined);
		setFailure(undefined);
	}
	function close() {
		if (submitting.current) return;
		reset();
		setOpen(false);
	}
	function handleSubmit() {
		if (submitting.current || !orgId) return;
		const parsed = createInvitationSchema.safeParse({
			email: email.trim(),
			role: role === InvitationRole.ADMIN ? "admin" : "member",
		});
		if (!parsed.success) {
			setEmailError("Enter a valid email address.");
			return;
		}
		setEmailError(undefined);
		setFailure(undefined);
		submitting.current = true;
		const invited = parsed.data.email;
		createInvitation.mutate(
			{ orgId, email: invited, role },
			{
				onSuccess: (response) => {
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
					if (response.acceptUrl)
						setLink({ url: response.acceptUrl, email: invited });
				},
				onError: (error) => {
					setFailure(
						refusalMessage(
							error,
							"Couldn't create the invitation. Try again; if this keeps happening, contact your administrator.",
						),
					);
					if (isMFARequired(error)) stepUpToast();
				},
				onSettled: () => {
					submitting.current = false;
				},
			},
		);
	}
	const footer = (
		<DialogFooter>
			<Button
				type="button"
				variant="outline"
				disabled={createInvitation.isPending}
				onClick={close}
			>
				Cancel
			</Button>
			<Button
				type="submit"
				form={formId}
				disabled={createInvitation.isPending || !orgId}
			>
				{createInvitation.isPending ? "Sending…" : "Send invite"}
			</Button>
		</DialogFooter>
	);

	return (
		<>
			<Dialog
				open={open}
				onOpenChange={(next) => {
					if (next) setOpen(true);
					else close();
				}}
			>
				<DialogTrigger render={<Button disabled={!orgId} />}>
					<Plus className="mr-2 h-4 w-4" />
					Invite
				</DialogTrigger>
				<DialogContent
					className="sm:max-w-md"
					showCloseButton={false}
					escape={footer}
				>
					<form
						id={formId}
						aria-label="Invite member"
						noValidate
						onSubmit={(event) => {
							event.preventDefault();
							handleSubmit();
						}}
					>
						<DialogHeader>
							<DialogTitle>Invite member</DialogTitle>
							<DialogDescription>
								Invite someone to join this organization.
							</DialogDescription>
						</DialogHeader>
						<div className="space-y-4 py-4">
							<Field label="Email" required error={emailError}>
								{(control) => (
									<Input
										{...control}
										type="email"
										autoComplete="email"
										placeholder="user@example.com"
										value={email}
										disabled={createInvitation.isPending}
										onChange={(event) => {
											setEmail(event.target.value);
											setEmailError(undefined);
										}}
									/>
								)}
							</Field>
							<Field
								label="Role"
								description="Members join the organization. Administrators can also manage its people and settings."
							>
								{(control) => (
									<Select
										disabled={createInvitation.isPending}
										items={{
											[String(InvitationRole.MEMBER)]: "Member",
											[String(InvitationRole.ADMIN)]: "Admin",
										}}
										value={String(role)}
										onValueChange={(value) => {
											if (value) setRole(Number(value) as InvitationRole);
										}}
									>
										<SelectTrigger {...control}>
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
								)}
							</Field>
							{failure && (
								<ErrorState title="Invitation not sent" detail={failure} />
							)}
						</div>
					</form>
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
