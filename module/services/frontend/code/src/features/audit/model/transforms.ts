import type { AuditEvent } from "./types";

export function formatAuditAction(action: string): string {
  return action.replace(/[._]/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
}

export interface AuditGroup {
  date: string;
  events: AuditEvent[];
}

export function groupByDate(events: AuditEvent[]): AuditGroup[] {
  const map = new Map<string, AuditEvent[]>();
  for (const event of events) {
    const date = event.createdAt
      ? new Date(event.createdAt).toLocaleDateString("en-US", {
          year: "numeric",
          month: "long",
          day: "numeric",
        })
      : "Unknown Date";
    if (!map.has(date)) map.set(date, []);
    map.get(date)!.push(event);
  }
  return Array.from(map.entries()).map(([date, events]) => ({ date, events }));
}
