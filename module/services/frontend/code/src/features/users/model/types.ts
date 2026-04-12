/** Clean domain types for the Users feature — decoupled from protobuf. */

export type UserStatus = "active" | "inactive" | "suspended" | "deleted" | "unspecified";

export interface User {
  uuid: string;
  primaryEmail: string;
  createdAt: string | undefined;
  updatedAt: string | undefined;
  lastLogin: string | undefined;
  status: UserStatus;
  profile: Record<string, string>;
  emailVerified: boolean;
}

export interface UserIdentity {
  uuid: string;
  userUuid: string;
  provider: string;
  providerId: string;
  providerEmail: string;
  createdAt: string | undefined;
  lastUsed: string | undefined;
  emailVerified: boolean;
}

/** Map proto enum int to domain string. */
export function toUserStatus(proto: number): UserStatus {
  switch (proto) {
    case 1: return "active";
    case 2: return "inactive";
    case 3: return "suspended";
    case 4: return "deleted";
    default: return "unspecified";
  }
}
