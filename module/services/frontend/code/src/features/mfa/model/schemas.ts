import { z } from "zod";

export const verifyTOTPSchema = z.object({
	code: z
		.string()
		.length(6, "Code must be exactly 6 digits")
		.regex(/^\d{6}$/, "Code must contain only digits"),
});

export type VerifyTOTPValues = z.infer<typeof verifyTOTPSchema>;
