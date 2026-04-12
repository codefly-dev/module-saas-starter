/** Clean domain types for the Teams feature. */

export type TeamRole = "owner" | "admin" | "member" | "unspecified";

export interface Team {
  id: string;
  orgId: string;
  name: string;
  description: string;
  createdAt: string | undefined;
}

export interface TeamMembership {
  teamId: string;
  userId: string;
  role: TeamRole;
  joinedAt: string | undefined;
}

/** Map proto enum int to domain string. */
export function toTeamRole(proto: number): TeamRole {
  switch (proto) {
    case 1: return "member";
    case 2: return "admin";
    case 3: return "owner";
    default: return "unspecified";
  }
}

/** Map domain string back to proto enum int. */
export function fromTeamRole(role: TeamRole): number {
  switch (role) {
    case "member": return 1;
    case "admin": return 2;
    case "owner": return 3;
    default: return 0;
  }
}
