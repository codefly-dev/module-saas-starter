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
	Label,
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
}

export function ImpersonateForm({
	open,
	userId,
	userEmail,
	onSubmit,
	onCancel,
	isPending,
}: ImpersonateFormProps) {
	const form = useForm<ImpersonateUserValues>({
		resolver: zodResolver(impersonateUserSchema),
		defaultValues: { userId, reason: "" },
	});

	return (
		<Dialog open={open} onOpenChange={(o) => !o && onCancel()}>
			<DialogContent className="sm:max-w-[425px]">
				<DialogHeader>
					<DialogTitle>Impersonate User</DialogTitle>
					<DialogDescription>
						Act as <span className="font-medium">{userEmail}</span>. Your
						justification is recorded on the audit trail alongside who you acted
						as and when.
					</DialogDescription>
				</DialogHeader>
				<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
					<div className="space-y-2">
						<Label htmlFor="reason">Justification</Label>
						<Textarea
							id="reason"
							placeholder="Ticket reference and what you need to reproduce."
							{...form.register("reason")}
						/>
						{form.formState.errors.reason && (
							<p className="text-sm text-destructive">
								{form.formState.errors.reason.message}
							</p>
						)}
					</div>
					<DialogFooter>
						<Button type="button" variant="outline" onClick={onCancel}>
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
