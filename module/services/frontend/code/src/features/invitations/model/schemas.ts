import { z } from "zod";

export const createInvitationSchema = z.object({
	email: z.string().email("Valid email required"),
	role: z.enum(["member", "admin", "owner"]).default("member"),
});

export type CreateInvitationInput = z.infer<typeof createInvitationSchema>;
