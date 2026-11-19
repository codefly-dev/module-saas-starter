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
import { type SuspendUserValues, suspendUserSchema } from "../model/schemas";

interface SuspendFormProps {
	open: boolean;
	userId: string;
	userEmail: string;
	onSubmit: (values: SuspendUserValues) => void;
	onCancel: () => void;
	isPending: boolean;
	error?: string;
}

export function SuspendForm({
	open,
	userId,
	userEmail,
	onSubmit,
	onCancel,
	isPending,
	error,
}: SuspendFormProps) {
	const form = useForm<SuspendUserValues>({
		resolver: zodResolver(suspendUserSchema),
		defaultValues: { userId, reason: "" },
	});

	return (
		<Dialog open={open} onOpenChange={(o) => !o && !isPending && onCancel()}>
			<DialogContent className="sm:max-w-[425px]">
				<DialogHeader>
					<DialogTitle>Suspend user</DialogTitle>
					<DialogDescription>
						Suspend <span className="font-medium">{userEmail}</span>. They will
						lose access until unsuspended.
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
					<Field label="Reason" error={form.formState.errors.reason?.message}>
						{(control) => (
							<Input
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
						<Button type="submit" variant="destructive" disabled={isPending}>
							{isPending ? "Suspending..." : "Suspend"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
