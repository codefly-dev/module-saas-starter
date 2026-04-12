"use client";

import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Button,
  Input,
  Label,
  Checkbox,
  Textarea,
} from "@/shared/ui";
import {
  createWebhookSchema,
  type CreateWebhookValues,
} from "../model/schemas";
import { WEBHOOK_EVENT_TYPES } from "../model/types";
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
  isPending,
}: WebhookFormProps) {
  const form = useForm<CreateWebhookValues>({
    resolver: zodResolver(createWebhookSchema),
    defaultValues: { url: "", events: [], description: "" },
  });

  const selectedEvents = form.watch("events");

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
              {WEBHOOK_EVENT_TYPES.map((event) => (
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
            <Button type="submit" disabled={isPending}>
              {isPending ? "Creating..." : "Create Webhook"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
