import { z } from "zod";

export const suspendUserSchema = z.object({
  userId: z.string().min(1, "User ID is required"),
  reason: z.string().min(1, "Reason is required").max(500, "Reason too long"),
});

export type SuspendUserValues = z.infer<typeof suspendUserSchema>;

/** Editable user fields — the profile name pair plus primary email. All optional;
 * an unchanged field is simply re-sent (UpdateUser replaces the profile map / email). */
export const editUserSchema = z.object({
  firstName: z.string().max(120, "Too long").optional(),
  lastName: z.string().max(120, "Too long").optional(),
  primaryEmail: z.string().email("Enter a valid email").optional().or(z.literal("")),
});

export type EditUserValues = z.infer<typeof editUserSchema>;
