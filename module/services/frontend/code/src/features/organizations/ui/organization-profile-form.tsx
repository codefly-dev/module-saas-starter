"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
	Input,
	Label,
} from "@/shared/ui";
import { lifecycleErrorMessage } from "../model/errors";
import { type CreateOrgValues, createOrgSchema } from "../model/schemas";
import { useUpdateOrganization } from "../service/lifecycle";

interface OrganizationProfileFormProps {
	organization: { id: string; name: string; slug: string };
}

/** Rename an organization or change its slug. */
export function OrganizationProfileForm({
	organization,
}: OrganizationProfileFormProps) {
	const update = useUpdateOrganization();
	const form = useForm<CreateOrgValues>({
		resolver: zodResolver(createOrgSchema),
		defaultValues: { name: organization.name, slug: organization.slug },
	});

	useEffect(() => {
		form.reset({ name: organization.name, slug: organization.slug });
	}, [organization.name, organization.slug, form]);

	const onSubmit = (values: CreateOrgValues) =>
		update.mutate(
			{ orgId: organization.id, name: values.name.trim(), slug: values.slug },
			{
				onSuccess: () => toast.success("Organization updated"),
				onError: (error) =>
					toast.error(
						lifecycleErrorMessage(error, "Couldn't update the organization"),
					),
			},
		);

	return (
		<Card>
			<form onSubmit={form.handleSubmit(onSubmit)}>
				<CardHeader>
					<CardTitle>Organization</CardTitle>
					<CardDescription>
						The name members see, and the slug that identifies it.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					<div className="space-y-2">
						<Label htmlFor={`org-name-${organization.id}`}>Name</Label>
						<Input
							id={`org-name-${organization.id}`}
							{...form.register("name")}
						/>
						{form.formState.errors.name && (
							<p className="text-sm text-destructive">
								{form.formState.errors.name.message}
							</p>
						)}
					</div>
					<div className="space-y-2">
						<Label htmlFor={`org-slug-${organization.id}`}>Slug</Label>
						<Input
							id={`org-slug-${organization.id}`}
							className="font-mono"
							{...form.register("slug")}
						/>
						{form.formState.errors.slug && (
							<p className="text-sm text-destructive">
								{form.formState.errors.slug.message}
							</p>
						)}
					</div>
				</CardContent>
				<CardFooter>
					<Button
						type="submit"
						disabled={update.isPending || !form.formState.isDirty}
					>
						{update.isPending ? "Saving..." : "Save"}
					</Button>
				</CardFooter>
			</form>
		</Card>
	);
}
