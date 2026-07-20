import { z } from "zod";

export const createKeySchema = z.object({
	name: z.string().min(1, "Key name is required"),
	environment: z.number().default(1),
});

export type CreateKeyInput = z.infer<typeof createKeySchema>;
