import { z } from "zod";

export const orgSettingsSchema = z.object({
  logoUrl: z.string().url("Must be a valid URL").or(z.literal("")),
  primaryColor: z
    .string()
    .regex(/^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/, "Must be a valid hex color")
    .or(z.literal("")),
  customDomain: z
    .string()
    .regex(/^([a-z0-9-]+\.)+[a-z]{2,}$/, "Must be a valid domain")
    .or(z.literal("")),
  faviconUrl: z.string().url("Must be a valid URL").or(z.literal("")),
});

export type OrgSettingsValues = z.infer<typeof orgSettingsSchema>;

export const defaultOrgSettings: OrgSettingsValues = {
  logoUrl: "",
  primaryColor: "#6366f1",
  customDomain: "",
  faviconUrl: "",
};
