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
import { type EditUserValues, editUserSchema } from "../model/schemas";
import { withDisplayName } from "../model/transforms";
import type { User } from "../model/types";
import type { UserEdit } from "../service/mutations";

interface EditUserFormProps {
	open: boolean;
	user: User;
	onSubmit: (edit: UserEdit) => void;
	onCancel: () => void;
	isPending: boolean;
	error?: string;
}

/** Edit a user's profile (name) and primary email. The name pair lives in the
 * `profile` map; we merge onto the existing profile so unrelated keys survive. */
export function EditUserForm({
	open,
	user,
	onSubmit,
	onCancel,
	isPending,
	error,
}: EditUserFormProps) {
	const form = useForm<EditUserValues>({
		resolver: zodResolver(editUserSchema),
		defaultValues: {
			firstName: user.profile.first_name ?? "",
			lastName: user.profile.last_name ?? "",
			primaryEmail: user.primaryEmail,
		},
	});

	const submit = (values: EditUserValues) => {
		if (isPending) return;
		const firstName = values.firstName?.trim() ?? "";
		const lastName = values.lastName?.trim() ?? "";
		const profile = withDisplayName(
			{
				...user.profile,
				first_name: firstName,
				last_name: lastName,
			},
			[firstName, lastName].filter(Boolean).join(" "),
		);
		// Always send a valid email (the User proto validates it); fall back to the
		// current address when the field was left unchanged/blank.
		onSubmit({
			uuid: user.uuid,
			profile,
			primaryEmail: values.primaryEmail?.trim() || user.primaryEmail,
		});
	};

	return (
		<Dialog open={open} onOpenChange={(o) => !o && !isPending && onCancel()}>
			<DialogContent className="sm:max-w-[425px]">
				<DialogHeader>
					<DialogTitle>Edit user</DialogTitle>
					<DialogDescription>
						Update the profile for{" "}
						<span className="font-medium">{user.primaryEmail}</span>.
					</DialogDescription>
				</DialogHeader>
				<form
					noValidate
					onSubmit={form.handleSubmit(submit)}
					className="space-y-4"
				>
					{error && <ErrorState title="Change wasn’t saved" detail={error} />}
					<Field
						label="First name"
						error={form.formState.errors.firstName?.message}
					>
						{(control) => (
							<Input
								{...control}
								disabled={isPending}
								type="text"
								{...form.register("firstName")}
							/>
						)}
					</Field>
					<Field
						label="Last name"
						error={form.formState.errors.lastName?.message}
					>
						{(control) => (
							<Input
								{...control}
								disabled={isPending}
								type="text"
								{...form.register("lastName")}
							/>
						)}
					</Field>
					<Field
						label="Primary email"
						error={form.formState.errors.primaryEmail?.message}
					>
						{(control) => (
							<Input
								{...control}
								disabled={isPending}
								type="email"
								{...form.register("primaryEmail")}
							/>
						)}
					</Field>

					<DialogFooter>
						<Button
							type="button"
							variant="outline"
							disabled={isPending}
							onClick={onCancel}
						>
							Cancel
						</Button>
						<Button type="submit" disabled={isPending}>
							{isPending ? "Saving..." : "Save changes"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
