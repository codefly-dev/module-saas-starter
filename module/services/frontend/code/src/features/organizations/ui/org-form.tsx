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
	Input,
	Label,
} from "@/shared/ui";
import { type CreateOrgValues, createOrgSchema } from "../model/schemas";
import { slugify } from "../model/transforms";

interface OrgFormProps {
	open: boolean;
	onSubmit: (values: CreateOrgValues) => void;
	onCancel: () => void;
	isPending: boolean;
}

export function OrgForm({ open, onSubmit, onCancel, isPending }: OrgFormProps) {
	const form = useForm<CreateOrgValues>({
		resolver: zodResolver(createOrgSchema),
		defaultValues: { name: "", slug: "" },
	});

	const handleNameChange = (e: React.ChangeEvent<HTMLInputElement>) => {
		const name = e.target.value;
		form.setValue("name", name);
		// Auto-generate slug from name if slug hasn't been manually edited
		const currentSlug = form.getValues("slug");
		const autoSlug = slugify(form.getValues("name").slice(0, -1) || "");
		if (
			!currentSlug ||
			currentSlug === autoSlug ||
			currentSlug === slugify(name)
		) {
			form.setValue("slug", slugify(name));
		}
	};

	return (
		<Dialog open={open} onOpenChange={(o) => !o && onCancel()}>
			<DialogContent className="sm:max-w-[425px]">
				<DialogHeader>
					<DialogTitle>Create Organization</DialogTitle>
					<DialogDescription>
						Add a new organization. You will be the owner.
					</DialogDescription>
				</DialogHeader>
				<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
					<div className="space-y-2">
						<Label htmlFor="org-name">Name</Label>
						<Input
							id="org-name"
							placeholder="Acme Inc."
							{...form.register("name")}
							onChange={handleNameChange}
						/>
						{form.formState.errors.name && (
							<p className="text-sm text-destructive">
								{form.formState.errors.name.message}
							</p>
						)}
					</div>
					<div className="space-y-2">
						<Label htmlFor="org-slug">Slug</Label>
						<Input
							id="org-slug"
							placeholder="acme-inc"
							{...form.register("slug")}
						/>
						{form.formState.errors.slug && (
							<p className="text-sm text-destructive">
								{form.formState.errors.slug.message}
							</p>
						)}
					</div>
					<DialogFooter>
						<Button type="button" variant="outline" onClick={onCancel}>
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
