import { z } from "zod";

export const createOrgSchema = z.object({
  name: z.string().min(1, "Name is required").max(100, "Name too long"),
  slug: z.string().min(1, "Slug is required").max(100, "Slug too long")
    .regex(/^[a-z0-9-]+$/, "Slug must be lowercase alphanumeric with hyphens"),
});

export type CreateOrgValues = z.infer<typeof createOrgSchema>;

export const addOrgMemberSchema = z.object({
  userId: z.string().min(1, "User ID is required"),
  role: z.enum(["member", "admin", "owner"]),
});

export type AddOrgMemberValues = z.infer<typeof addOrgMemberSchema>;
