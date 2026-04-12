import type { WebhookDeliveryStatus } from "./types";

type BadgeVariant = "default" | "secondary" | "destructive" | "outline";

export function formatDeliveryStatus(status: WebhookDeliveryStatus): {
  label: string;
  variant: BadgeVariant;
} {
  switch (status) {
    case "success":
      return { label: "Success", variant: "default" };
    case "pending":
      return { label: "Pending", variant: "secondary" };
    case "retrying":
      return { label: "Retrying", variant: "outline" };
    case "failed":
      return { label: "Failed", variant: "destructive" };
    default:
      return { label: "Unknown", variant: "secondary" };
  }
}

export function formatEventType(event: string): string {
  return event
    .split(".")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}
