import { z } from "zod";

export const createWebhookSchema = z.object({
	url: z.string().url("Must be a valid URL").min(1, "URL is required"),
	events: z.array(z.string()).min(1, "Select at least one event"),
	description: z.string().max(500, "Description too long").optional(),
});

export type CreateWebhookValues = z.infer<typeof createWebhookSchema>;
