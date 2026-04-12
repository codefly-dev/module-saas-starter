/** Shape of module/fixtures/*.yaml — shared between server loader and client. */

import { z } from "zod";

export const FixtureUserSchema = z.object({
  email: z.string().email(),
  name: z.string().min(1),
  role: z.string().min(1),
  provider: z.string().min(1),
  provider_id: z.string().min(1),
});

export const FixtureOrgMemberSchema = z.object({
  email: z.string().email(),
  role: z.string().min(1),
});

export const FixtureOrgSchema = z.object({
  name: z.string().min(1),
  owner: z.string().email(),
  members: z.array(FixtureOrgMemberSchema),
});

export const FixtureTeamSchema = z.object({
  name: z.string().min(1),
  org: z.string().min(1),
  members: z.array(z.string()),
});

export const FixtureFileSchema = z.object({
  users: z.array(FixtureUserSchema),
  organizations: z.array(FixtureOrgSchema).optional(),
  teams: z.array(FixtureTeamSchema).optional(),
});

export type FixtureUser = z.infer<typeof FixtureUserSchema>;
export type FixtureOrgMember = z.infer<typeof FixtureOrgMemberSchema>;
export type FixtureOrg = z.infer<typeof FixtureOrgSchema>;
export type FixtureTeam = z.infer<typeof FixtureTeamSchema>;
export type FixtureFile = z.infer<typeof FixtureFileSchema>;
