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
import { type SuspendUserValues, suspendUserSchema } from "../model/schemas";

interface SuspendFormProps {
	open: boolean;
	userId: string;
	userEmail: string;
	onSubmit: (values: SuspendUserValues) => void;
	onCancel: () => void;
	isPending: boolean;
}

export function SuspendForm({
	open,
	userId,
	userEmail,
	onSubmit,
	onCancel,
	isPending,
}: SuspendFormProps) {
	const form = useForm<SuspendUserValues>({
		resolver: zodResolver(suspendUserSchema),
		defaultValues: { userId, reason: "" },
	});

	return (
		<Dialog open={open} onOpenChange={(o) => !o && onCancel()}>
			<DialogContent className="sm:max-w-[425px]">
				<DialogHeader>
					<DialogTitle>Suspend User</DialogTitle>
					<DialogDescription>
						Suspend <span className="font-medium">{userEmail}</span>. They will
						lose access until unsuspended.
					</DialogDescription>
				</DialogHeader>
				<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
					<div className="space-y-2">
						<Label htmlFor="reason">Reason</Label>
						<Input
							id="reason"
							placeholder="Policy violation, etc."
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
						<Button type="submit" variant="destructive" disabled={isPending}>
							{isPending ? "Suspending..." : "Suspend"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
