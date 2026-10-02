"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";
import { UserPicker } from "@/components/user-picker";
import { lifecycleErrorMessage } from "@/features/organizations/model/errors";
import { slugify } from "@/features/organizations/model/transforms";
import { orgMutations } from "@/features/organizations/service/mutations";
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

interface CreateOrganizationDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
}

/**
 * A platform administrator creating an organization for someone: the owner is
 * picked from the user directory, and is you when left empty. Only a platform
 * super administrator may name another owner; the server refuses anyone else.
 */
export function CreateOrganizationDialog({
	open,
	onOpenChange,
}: CreateOrganizationDialogProps) {
	const queryClient = useQueryClient();
	const [name, setName] = useState("");
	const [slug, setSlug] = useState("");
	const [slugEdited, setSlugEdited] = useState(false);
	const [ownerId, setOwnerId] = useState("");

	const reset = () => {
		setName("");
		setSlug("");
		setSlugEdited(false);
		setOwnerId("");
	};

	const create = useMutation({
		mutationFn: () => orgMutations.create(name.trim(), slug.trim(), ownerId),
		onSuccess: () => {
			toast.success(`Created ${name.trim()}`);
			queryClient.invalidateQueries({ queryKey: ["platform-organizations"] });
			queryClient.invalidateQueries({ queryKey: ["organizations"] });
			reset();
			onOpenChange(false);
		},
		onError: (error) =>
			toast.error(
				lifecycleErrorMessage(error, "Couldn't create the organization"),
			),
	});

	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				if (!next) reset();
				onOpenChange(next);
			}}
		>
			<DialogContent className="sm:max-w-md">
				<DialogHeader>
					<DialogTitle>Create organization</DialogTitle>
					<DialogDescription>
						Create an organization and choose who owns it.
					</DialogDescription>
				</DialogHeader>
				<div className="space-y-4">
					<div className="space-y-2">
						<Label htmlFor="platform-org-name">Name</Label>
						<Input
							id="platform-org-name"
							value={name}
							onChange={(event) => {
								setName(event.target.value);
								if (!slugEdited) setSlug(slugify(event.target.value));
							}}
						/>
					</div>
					<div className="space-y-2">
						<Label htmlFor="platform-org-slug">Slug</Label>
						<Input
							id="platform-org-slug"
							className="font-mono"
							value={slug}
							onChange={(event) => {
								setSlugEdited(true);
								setSlug(event.target.value);
							}}
						/>
					</div>
					<div className="space-y-2">
						<p className="text-sm font-medium">Owner</p>
						<UserPicker value={ownerId} onChange={setOwnerId} />
						<p className="text-sm text-muted-foreground">
							Leave empty to own it yourself.
						</p>
					</div>
				</div>
				<DialogFooter>
					<Button variant="outline" onClick={() => onOpenChange(false)}>
						Cancel
					</Button>
					<Button
						disabled={!name.trim() || !slug.trim() || create.isPending}
						onClick={() => create.mutate()}
					>
						{create.isPending ? "Creating..." : "Create"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
