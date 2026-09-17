import { z } from "zod";

export const suspendUserSchema = z.object({
	userId: z.string().min(1, "User ID is required"),
	reason: z.string().min(1, "Reason is required").max(500, "Reason too long"),
});

export type SuspendUserValues = z.infer<typeof suspendUserSchema>;

/** The justification recorded on the impersonation audit event. Both bounds
 * mirror the request contract, and both are measured the way the server measures
 * them: in code points, on the trimmed value. `String.length` counts UTF-16
 * units, so it disagrees with the server on anything outside the basic plane —
 * five emoji read as ten there and five here, which would let the dialog accept
 * a reason the RPC then rejects with no field to attach the error to. */
const codePoints = (value: string) => [...value].length;

export const impersonateUserSchema = z.object({
	userId: z.string().min(1, "User ID is required"),
	reason: z
		.string()
		.trim()
		.refine(
			(value) => codePoints(value) >= 10,
			"Describe why, in at least 10 characters",
		)
		.refine((value) => codePoints(value) <= 500, "Reason too long"),
});

export type ImpersonateUserValues = z.infer<typeof impersonateUserSchema>;

/** Editable user fields — the profile name pair plus primary email. All optional;
 * an unchanged field is simply re-sent (UpdateUser replaces the profile map / email). */
export const editUserSchema = z.object({
	firstName: z.string().max(120, "Too long").optional(),
	lastName: z.string().max(120, "Too long").optional(),
	primaryEmail: z
		.string()
		.email("Enter a valid email")
		.optional()
		.or(z.literal("")),
});

export type EditUserValues = z.infer<typeof editUserSchema>;
