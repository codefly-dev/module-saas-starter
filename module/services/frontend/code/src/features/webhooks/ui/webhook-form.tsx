"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useAuditEventTypes } from "@/features/audit/service/queries";
import { failureText } from "@/shared/lib/failure-text";
import {
	Button,
	Checkbox,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	ErrorState,
	Field,
	Input,
	Label,
	Spinner,
	Textarea,
	useLoadingPhase,
} from "@/shared/ui";
import {
	type CreateWebhookValues,
	createWebhookSchema,
} from "../model/schemas";
import { formatEventType } from "../model/transforms";

interface WebhookFormProps {
	open: boolean;
	onSubmit: (values: CreateWebhookValues) => void;
	onCancel: () => void;
	isPending: boolean;
	error?: string;
}

export function WebhookForm({
	open,
	onSubmit,
	onCancel,
	isPending: isSubmitting,
	error,
}: WebhookFormProps) {
	const form = useForm<CreateWebhookValues>({
		resolver: zodResolver(createWebhookSchema),
		defaultValues: { url: "", events: [], description: "" },
	});
	const [search, setSearch] = useState("");
	const selectedEvents = form.watch("events");
	const registry = useAuditEventTypes({ enabled: open });
	const { quiet, indicator } = useLoadingPhase(open && registry.isPending);
	// Eligibility remains the registry's decision, including runtime contributions.
	const events = (registry.data ?? [])
		.filter((event) => event.webhookEligible && !event.deprecated)
		.sort((a, b) => a.name.localeCompare(b.name));
	const visibleEvents = events.filter((event) =>
		(event.description || formatEventType(event.name))
			.toLowerCase()
			.includes(search.trim().toLowerCase()),
	);
	const unavailable =
		registry.isPending || registry.isError || events.length === 0;
	const submit = (values: CreateWebhookValues) => {
		if (isSubmitting || unavailable) return;
		if (
			values.events.some((name) => !events.some((event) => event.name === name))
		) {
			form.setError("events", {
				message: "The available events changed. Review your selection.",
			});
			return;
		}
		onSubmit(values);
	};
	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				if (!next && !isSubmitting) onCancel();
			}}
		>
			<DialogContent className="sm:max-w-lg">
				<DialogHeader>
					<DialogTitle>Create webhook</DialogTitle>
					<DialogDescription>
						Send selected activity updates to another application.
					</DialogDescription>
				</DialogHeader>
				<form
					noValidate
					onSubmit={form.handleSubmit(submit)}
					className="space-y-4"
				>
					{error && (
						<ErrorState title="Webhook wasn’t created" detail={error} />
					)}
					<Field
						label="Endpoint URL"
						error={form.formState.errors.url?.message}
					>
						{(control) => (
							<Input
								{...control}
								disabled={isSubmitting}
								placeholder="https://example.com/webhooks"
								{...form.register("url")}
							/>
						)}
					</Field>
					<Field
						label="Description"
						description="Optional. Explain what this webhook is used for."
						error={form.formState.errors.description?.message}
					>
						{(control) => (
							<Textarea
								{...control}
								disabled={isSubmitting}
								rows={2}
								{...form.register("description")}
							/>
						)}
					</Field>
					<fieldset
						className="space-y-2"
						aria-describedby={
							form.formState.errors.events ? "webhook-events-error" : undefined
						}
					>
						<legend className="type-label">Events</legend>
						{!registry.isError && !registry.isPending && events.length > 0 && (
							<Field label="Search events">
								{(control) => (
									<Input
										{...control}
										value={search}
										onChange={(event) => setSearch(event.target.value)}
										disabled={isSubmitting}
										placeholder="Find an event"
									/>
								)}
							</Field>
						)}
						{form.formState.errors.events && (
							<p
								id="webhook-events-error"
								role="alert"
								className="text-sm text-destructive"
							>
								{form.formState.errors.events.message}
							</p>
						)}
						{indicator ? (
							<Spinner label="Loading available events" />
						) : quiet ? null : registry.isError ? (
							<ErrorState
								title="Events unavailable"
								detail={failureText(
									"load the available events",
									registry.error,
								)}
							/>
						) : events.length === 0 ? (
							<p className="text-sm text-muted-foreground">
								No events are available to send. Ask your administrator to
								enable events for this organization.
							</p>
						) : (
							<div className="grid max-h-48 grid-cols-1 gap-3 overflow-y-auto rounded-md border p-3 sm:grid-cols-2">
								{visibleEvents.length === 0 && (
									<p className="text-sm text-muted-foreground">
										No events match your search. Try another word.
									</p>
								)}
								{visibleEvents.map((event) => (
									<div
										key={event.name}
										className="flex items-start gap-2 leading-relaxed"
									>
										<Checkbox
											id={`webhook-event-${event.name}`}
											disabled={isSubmitting}
											checked={selectedEvents.includes(event.name)}
											onCheckedChange={(checked) =>
												form.setValue(
													"events",
													checked
														? [...selectedEvents, event.name]
														: selectedEvents.filter(
																(name) => name !== event.name,
															),
													{ shouldValidate: true },
												)
											}
										/>
										<Label
											htmlFor={`webhook-event-${event.name}`}
											className="leading-relaxed"
										>
											{event.description || formatEventType(event.name)}
										</Label>
									</div>
								))}
							</div>
						)}
					</fieldset>
					<DialogFooter>
						<Button
							type="button"
							variant="outline"
							disabled={isSubmitting}
							onClick={onCancel}
						>
							Cancel
						</Button>
						<Button type="submit" disabled={isSubmitting || unavailable}>
							{isSubmitting ? "Creating…" : "Create webhook"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
