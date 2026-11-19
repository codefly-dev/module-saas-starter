"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { Controller, useForm, useFormState } from "react-hook-form";
import { toast } from "sonner";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
	ErrorState,
	Field,
	Input,
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

	const { isDirty } = useFormState({ control: form.control });
	const { reset } = form;
	useEffect(() => {
		reset({ name: organization.name, slug: organization.slug });
	}, [organization.name, organization.slug, reset]);

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
			<form
				noValidate
				onSubmit={form.handleSubmit((values) => {
					if (!update.isPending) onSubmit(values);
				})}
			>
				<CardHeader>
					<CardTitle role="heading" aria-level={2}>
						Organization
					</CardTitle>
					<CardDescription>
						The name members see and the address that identifies this
						organization.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					<Field
						label="Name"
						error={form.formState.errors.name?.message}
						required
					>
						{(control) => (
							<Controller
								name="name"
								control={form.control}
								render={({ field }) => <Input {...control} {...field} disabled={update.isPending} />}
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
							<Controller
								name="slug"
								control={form.control}
								render={({ field }) => <Input {...control} {...field} disabled={update.isPending} />}
							/>
						)}
					</Field>
					{update.isError && (
						<ErrorState
							title="Couldn't update the organization"
							detail={lifecycleErrorMessage(
								update.error,
								"Check your connection and try again.",
							)}
						/>
					)}
				</CardContent>
				<CardFooter>
					<Button
						type="submit"
						disabled={update.isPending || !isDirty}
					>
						{update.isPending ? "Saving..." : "Save"}
					</Button>
				</CardFooter>
			</form>
		</Card>
	);
}
