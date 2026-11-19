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
	Textarea,
} from "@/shared/ui";
import { type CreateTeamValues, createTeamSchema } from "../model/schemas";

interface TeamFormProps {
	open: boolean;
	/** "create" (default) or "edit" — an edit form seeds `initial` and relabels. */
	mode?: "create" | "edit";
	initial?: { name: string; description?: string };
	onSubmit: (values: CreateTeamValues) => void;
	onCancel: () => void;
	isPending: boolean;
	error?: string;
	existingTeam?: { name: string; onUse: () => void };
}

export function TeamForm({
	open,
	mode = "create",
	initial,
	onSubmit,
	onCancel,
	isPending,
	error,
	existingTeam,
}: TeamFormProps) {
	const editing = mode === "edit";
	const form = useForm({
		resolver: zodResolver(createTeamSchema),
		defaultValues: {
			name: initial?.name ?? "",
			description: initial?.description ?? "",
		},
	});

	return (
		<Dialog open={open} onOpenChange={(o) => !o && !isPending && onCancel()}>
			<DialogContent className="sm:max-w-[425px]">
				<DialogHeader>
					<DialogTitle>{editing ? "Rename team" : "Create team"}</DialogTitle>
					<DialogDescription>
						{editing
							? "Update this team's name and description."
							: "Add a new team to the selected organization."}
					</DialogDescription>
				</DialogHeader>
				<form
					noValidate
					onSubmit={form.handleSubmit((values) => {
						if (!isPending) onSubmit(values);
					})}
					className="space-y-4"
				>
					{error && <ErrorState title="Team wasn’t saved" detail={error} />}
					{existingTeam && (
						<Button
							type="button"
							variant="outline"
							disabled={isPending}
							onClick={existingTeam.onUse}
						>
							Use existing {existingTeam.name}
						</Button>
					)}

					<Field label="Name" error={form.formState.errors.name?.message}>
						{(control) => (
							<Input
								{...control}
								disabled={isPending}
								placeholder="Engineering"
								{...form.register("name")}
							/>
						)}
					</Field>
					<Field
						label="Description"
						description="Optional. Describe what this team does."
						error={form.formState.errors.description?.message}
					>
						{(control) => (
							<Textarea
								{...control}
								disabled={isPending}
								{...form.register("description")}
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
							{isPending
								? editing
									? "Saving..."
									: "Creating..."
								: editing
									? "Save"
									: "Create"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
