import { z } from "zod";

export const suspendUserSchema = z.object({
  userId: z.string().min(1, "User ID is required"),
  reason: z.string().min(1, "Reason is required").max(500, "Reason too long"),
});

export type SuspendUserValues = z.infer<typeof suspendUserSchema>;
