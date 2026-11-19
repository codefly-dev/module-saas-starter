"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { toast } from "sonner";
import { UserPicker } from "@/components/user-picker";
import { lifecycleErrorMessage } from "@/features/organizations/model/errors";
import { createOrgSchema } from "@/features/organizations/model/schemas";
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
	ErrorState,
	Field,
	Input,
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
	const busy = useRef(false);
	const [errors, setErrors] = useState<{ name?: string; slug?: string }>({});
	const [name, setName] = useState("");
	const [slug, setSlug] = useState("");
	const [slugEdited, setSlugEdited] = useState(false);
	const [ownerId, setOwnerId] = useState("");

	const reset = () => {
		setErrors({});
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
		onSettled: () => {
			busy.current = false;
		},
	});

	const close = () => {
		if (busy.current || create.isPending) return;
		reset();
		create.reset();
		onOpenChange(false);
	};
	const submit = () => {
		if (busy.current || create.isPending) return;
		const parsed = createOrgSchema.safeParse({
			name: name.trim(),
			slug: slug.trim(),
		});
		if (!parsed.success) {
			const fields = parsed.error.flatten().fieldErrors;
			setErrors({ name: fields.name?.[0], slug: fields.slug?.[0] });
			return;
		}
		setErrors({});
		busy.current = true;
		create.mutate();
	};

	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				if (!next) close();
			}}
		>
			<DialogContent className="sm:max-w-md">
				<DialogHeader>
					<DialogTitle>Create organization</DialogTitle>
					<DialogDescription>
						Create an organization and choose who owns it.
					</DialogDescription>
				</DialogHeader>
				<form
					id="create-platform-organization"
					noValidate
					onSubmit={(event) => {
						event.preventDefault();
						submit();
					}}
					className="space-y-4"
				>
					<Field label="Name" error={errors.name}>
						{(control) => (
							<Input
								{...control}
								disabled={create.isPending}
								value={name}
								onChange={(event) => {
									setName(event.target.value);
									if (!slugEdited) setSlug(slugify(event.target.value));
								}}
							/>
						)}
					</Field>
					<Field
						label="Organization address"
						error={errors.slug}
						description="Use lowercase letters, numbers and hyphens."
					>
						{(control) => (
							<Input
								{...control}
								disabled={create.isPending}
								className="font-mono"
								value={slug}
								onChange={(event) => {
									setSlugEdited(true);
									setSlug(event.target.value);
								}}
							/>
						)}
					</Field>
					<div className="space-y-2">
						<p className="text-sm font-medium">Owner</p>
						<UserPicker
							value={ownerId}
							onChange={setOwnerId}
							disabled={create.isPending}
						/>
						<p className="text-sm text-muted-foreground">
							Leave empty to own it yourself.
						</p>
					</div>
				</form>
				{create.isError && (
					<ErrorState
						title="Organization wasn’t created"
						detail={lifecycleErrorMessage(
							create.error,
							"Your entries have been kept. Try creating the organization again.",
						)}
					/>
				)}
				<DialogFooter>
					<Button variant="outline" disabled={create.isPending} onClick={close}>
						Cancel
					</Button>
					<Button
						type="submit"
						form="create-platform-organization"
						disabled={create.isPending}
					>
						{create.isPending ? "Creating..." : "Create"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
