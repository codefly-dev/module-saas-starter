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
	Textarea,
} from "@/shared/ui";
import {
	type ImpersonateUserValues,
	impersonateUserSchema,
} from "../model/schemas";

interface ImpersonateFormProps {
	open: boolean;
	userId: string;
	userEmail: string;
	onSubmit: (values: ImpersonateUserValues) => void;
	onCancel: () => void;
	isPending: boolean;
	error?: string;
}

export function ImpersonateForm({
	open,
	userId,
	userEmail,
	onSubmit,
	onCancel,
	isPending,
	error,
}: ImpersonateFormProps) {
	const form = useForm<ImpersonateUserValues>({
		resolver: zodResolver(impersonateUserSchema),
		defaultValues: { userId, reason: "" },
	});

	return (
		<Dialog open={open} onOpenChange={(o) => !o && !isPending && onCancel()}>
			<DialogContent className="sm:max-w-[425px]">
				<DialogHeader>
					<DialogTitle>Act as user</DialogTitle>
					<DialogDescription>
						Act as <span className="font-medium">{userEmail}</span>. Your
						justification is recorded on the audit trail alongside who you acted
						as and when.
					</DialogDescription>
				</DialogHeader>
				<form
					noValidate
					onSubmit={form.handleSubmit((values) => {
						if (!isPending) onSubmit(values);
					})}
					className="space-y-4"
				>
					{error && <ErrorState title="Change wasn’t saved" detail={error} />}
					<Field
						label="Justification"
						error={form.formState.errors.reason?.message}
					>
						{(control) => (
							<Textarea
								{...control}
								disabled={isPending}
								{...form.register("reason")}
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
							{isPending ? "Starting..." : "Start session"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
