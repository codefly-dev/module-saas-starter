"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useForm } from "react-hook-form";
import {
	Button,
	Checkbox,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	Input,
	Label,
	Textarea,
} from "@/shared/ui";
import {
	type CreateWebhookValues,
	createWebhookSchema,
} from "../model/schemas";
import { useAuditEventTypes } from "@/features/audit/service/queries";
import { formatEventType } from "../model/transforms";

interface WebhookFormProps {
	open: boolean;
	onSubmit: (values: CreateWebhookValues) => void;
	onCancel: () => void;
	isPending: boolean;
}

export function WebhookForm({
	open,
	onSubmit,
	onCancel,
	isPending: isSubmitting,
}: WebhookFormProps) {
	const form = useForm<CreateWebhookValues>({
		resolver: zodResolver(createWebhookSchema),
		defaultValues: { url: "", events: [], description: "" },
	});

	const selectedEvents = form.watch("events");

	// The registry decides what may be offered, not this build: only a type it
	// reports as webhook-eligible can ever reach an endpoint, and CreateSubscription
	// refuses any other name. Fetched only while the dialog is open, and sorted so
	// the order does not depend on the server's.
	const { data, isPending, isError } = useAuditEventTypes({ enabled: open });
	const eventTypes = (data ?? [])
		.filter((type) => type.webhookEligible && !type.deprecated)
		.map((type) => type.name)
		.sort();

	const toggleEvent = (event: string) => {
		const current = form.getValues("events");
		if (current.includes(event)) {
			form.setValue(
				"events",
				current.filter((e) => e !== event),
				{ shouldValidate: true },
			);
		} else {
			form.setValue("events", [...current, event], { shouldValidate: true });
		}
	};

	return (
		<Dialog open={open} onOpenChange={(o) => !o && onCancel()}>
			<DialogContent className="sm:max-w-[500px]">
				<DialogHeader>
					<DialogTitle>Create Webhook</DialogTitle>
					<DialogDescription>
						Subscribe to events and receive HTTP POST callbacks.
					</DialogDescription>
				</DialogHeader>
				<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
					<div className="space-y-2">
						<Label htmlFor="url">Endpoint URL</Label>
						<Input
							id="url"
							placeholder="https://example.com/webhooks"
							{...form.register("url")}
						/>
						{form.formState.errors.url && (
							<p className="text-sm text-destructive">
								{form.formState.errors.url.message}
							</p>
						)}
					</div>

					<div className="space-y-2">
						<Label htmlFor="description">Description (optional)</Label>
						<Textarea
							id="description"
							placeholder="What this webhook is used for..."
							{...form.register("description")}
							className="resize-none"
							rows={2}
						/>
					</div>

					<div className="space-y-2">
						<Label>Events</Label>
						{form.formState.errors.events && (
							<p className="text-sm text-destructive">
								{form.formState.errors.events.message}
							</p>
						)}
						<div className="grid grid-cols-2 gap-2 rounded-md border p-3 max-h-48 overflow-y-auto">
							{isPending && (
								<p className="col-span-2 text-sm text-muted-foreground">
									Loading event types...
								</p>
							)}
							{isError && (
								<p className="col-span-2 text-sm text-destructive">
									Event types could not be loaded. Try again in a moment.
								</p>
							)}
							{!isPending && !isError && eventTypes.length === 0 && (
								<p className="col-span-2 text-sm text-muted-foreground">
									No event type in this deployment can be delivered to an
									endpoint.
								</p>
							)}
							{eventTypes.map((event) => (
								<label
									key={event}
									className="flex items-center gap-2 text-sm cursor-pointer"
								>
									<Checkbox
										checked={selectedEvents.includes(event)}
										onCheckedChange={() => toggleEvent(event)}
									/>
									{formatEventType(event)}
								</label>
							))}
						</div>
					</div>

					<DialogFooter>
						<Button type="button" variant="outline" onClick={onCancel}>
							Cancel
						</Button>
						<Button type="submit" disabled={isSubmitting}>
							{isSubmitting ? "Creating..." : "Create Webhook"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
