/** Clean domain types for the Organizations feature. */

export type OrgRole = "owner" | "admin" | "member" | "unspecified";

export interface Organization {
  id: string;
  name: string;
  slug: string;
  ownerId: string;
  createdAt: string | undefined;
}

export interface OrgMembership {
  orgId: string;
  userId: string;
  role: OrgRole;
  joinedAt: string | undefined;
}

/** Map proto enum int to domain string. */
export function toOrgRole(proto: number): OrgRole {
  switch (proto) {
    case 1: return "member";
    case 2: return "admin";
    case 3: return "owner";
    default: return "unspecified";
  }
}

/** Map domain string back to proto enum int. */
export function fromOrgRole(role: OrgRole): number {
  switch (role) {
    case "member": return 1;
    case "admin": return 2;
    case "owner": return 3;
    default: return 0;
  }
}
