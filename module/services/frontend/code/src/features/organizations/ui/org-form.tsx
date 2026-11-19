"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useForm } from "react-hook-form";
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
import { type CreateOrgValues, createOrgSchema } from "../model/schemas";
import { slugify } from "../model/transforms";

interface OrgFormProps {
	open: boolean;
	onSubmit: (values: CreateOrgValues) => void;
	onCancel: () => void;
	isPending: boolean;
	error?: string;
}

export function OrgForm({
	open,
	onSubmit,
	onCancel,
	isPending,
	error,
}: OrgFormProps) {
	const form = useForm<CreateOrgValues>({
		resolver: zodResolver(createOrgSchema),
		defaultValues: { name: "", slug: "" },
	});

	const handleNameChange = (e: React.ChangeEvent<HTMLInputElement>) => {
		const name = e.target.value;
		const previousName = form.getValues("name");
		form.setValue("name", name, { shouldValidate: form.formState.isSubmitted });
		// Auto-generate slug from name if slug hasn't been manually edited
		const currentSlug = form.getValues("slug");
		const autoSlug = slugify(previousName);
		if (
			!currentSlug ||
			currentSlug === autoSlug ||
			currentSlug === slugify(name)
		) {
			form.setValue("slug", slugify(name), {
				shouldValidate: form.formState.isSubmitted,
			});
		}
	};

	return (
		<Dialog open={open} onOpenChange={(o) => !o && !isPending && onCancel()}>
			<DialogContent className="sm:max-w-[425px]">
				<DialogHeader>
					<DialogTitle>Create organization</DialogTitle>
					<DialogDescription>
						Add a new organization. You will be the owner.
					</DialogDescription>
				</DialogHeader>
				<form
					noValidate
					onSubmit={form.handleSubmit((values) => {
						if (!isPending) onSubmit(values);
					})}
					className="space-y-4"
				>
					<Field
						label="Name"
						error={form.formState.errors.name?.message}
						required
					>
						{(control) => (
							<Input
								{...control}
								placeholder="Acme Inc."
								{...form.register("name")}
								onChange={handleNameChange}
								disabled={isPending}
							/>
						)}
					</Field>
					<Field
						label="Organization address"
						description="Use lowercase letters, numbers and hyphens."
						error={form.formState.errors.slug?.message}
						required
					>
						{(control) => (
							<Input
								{...control}
								placeholder="acme-inc"
								{...form.register("slug")}
								disabled={isPending}
							/>
						)}
					</Field>
					{error && (
						<ErrorState
							title="Couldn't create the organization"
							detail={error}
						/>
					)}
					<DialogFooter>
						<Button
							type="button"
							variant="outline"
							onClick={onCancel}
							disabled={isPending}
						>
							Cancel
						</Button>
						<Button type="submit" disabled={isPending}>
							{isPending ? "Creating..." : "Create"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
