import { z } from "zod";

export const permissionSchema = z.object({
  resource: z.string().min(1, "Resource is required"),
  action: z.string().min(1, "Action is required"),
});

export const createRoleSchema = z.object({
  name: z.string().min(1, "Role name is required"),
  description: z.string().optional(),
  permissions: z.array(permissionSchema).default([]),
});

export type CreateRoleInput = z.infer<typeof createRoleSchema>;

export const assignRoleSchema = z.object({
  subjectId: z.string().min(1, "Subject ID is required"),
  roleId: z.string().min(1, "Role ID is required"),
  orgId: z.string().optional(),
});

export type AssignRoleInput = z.infer<typeof assignRoleSchema>;
