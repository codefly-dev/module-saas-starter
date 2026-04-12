import type { InvitationStatusValue } from "./types";

const STATUS_MAP: Record<number, InvitationStatusValue> = {
  0: "unspecified",
  1: "pending",
  2: "accepted",
  3: "revoked",
  4: "expired",
};

export function formatInvitationStatus(status: number): {
  label: string;
  variant: "default" | "secondary" | "destructive" | "outline";
} {
  const value = STATUS_MAP[status] ?? "unspecified";
  switch (value) {
    case "pending":
      return { label: "Pending", variant: "default" };
    case "accepted":
      return { label: "Accepted", variant: "secondary" };
    case "revoked":
      return { label: "Revoked", variant: "destructive" };
    case "expired":
      return { label: "Expired", variant: "outline" };
    default:
      return { label: "Unknown", variant: "outline" };
  }
}
