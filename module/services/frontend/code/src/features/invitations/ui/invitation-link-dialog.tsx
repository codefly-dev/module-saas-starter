"use client";

import { Copy } from "lucide-react";
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

interface InvitationLinkDialogProps {
	/** The accept link; the dialog is open while it is set. */
	url: string | null;
	email: string;
	onClose: () => void;
}

/**
 * Hands the administrator an invitation's accept link, so an invitation never
 * depends on email being delivered. The link is shown once — the server keeps
 * only its hash — so it is offered here to copy, and issuing another one later
 * makes this one stop working.
 */
export function InvitationLinkDialog({
	url,
	email,
	onClose,
}: InvitationLinkDialogProps) {
	const copy = async () => {
		if (!url) return;
		try {
			await navigator.clipboard.writeText(url);
			toast.success("Invitation link copied");
		} catch {
			toast.error("Couldn't copy — select the link and copy it by hand");
		}
	};
	return (
		<Dialog open={url !== null} onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="sm:max-w-lg">
				<DialogHeader>
					<DialogTitle>Invitation link</DialogTitle>
					<DialogDescription>
						Share this link with {email}. It works like the invitation email and
						expires with the invitation.
					</DialogDescription>
				</DialogHeader>
				<div className="space-y-2">
					<Label htmlFor="invitation-link">Accept link</Label>
					<div className="flex gap-2">
						<Input
							id="invitation-link"
							readOnly
							value={url ?? ""}
							className="font-mono text-xs"
							onFocus={(event) => event.currentTarget.select()}
						/>
						<Button variant="outline" onClick={() => void copy()}>
							<Copy className="mr-2 h-4 w-4" />
							Copy
						</Button>
					</div>
					<p className="text-sm text-muted-foreground">
						This is the only time this link is shown. Copying a new link later
						makes this one, and any emailed one, stop working.
					</p>
				</div>
				<DialogFooter>
					<Button onClick={onClose}>Done</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
