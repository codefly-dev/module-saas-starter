import { useMutation } from "@tanstack/react-query";
import { useAuditService } from "@/lib/hooks/use-api-client";
import type { AuditEvent } from "../model/types";

/**
 * Export audit log as CSV or JSON and trigger a browser download.
 *
 * Since the backend AuditService only exposes QueryAuditLog, we fetch the
 * events client-side and serialize them into the chosen format.
 */
export function useExportAuditLog() {
	const svc = useAuditService();

	return useMutation({
		mutationFn: async ({
			format,
			eventType,
			orgId,
		}: {
			format: "csv" | "json";
			eventType?: string;
			orgId?: string;
		}) => {
			const resp = await svc.queryAuditLog({
				orgId: orgId ?? "",
				eventType: eventType ?? "",
				actorId: "",
				pageSize: 10_000,
			});

			const events = (resp.events ?? []) as AuditEvent[];
			let data: string;
			let mimeType: string;
			let extension: string;

			if (format === "json") {
				data = JSON.stringify(events, null, 2);
				mimeType = "application/json";
				extension = "json";
			} else {
				const headers = [
					"id",
					"actorId",
					"actorType",
					"eventType",
					"category",
					"resource",
					"resourceId",
					"orgId",
					"ipAddress",
					"createdAt",
				];
				const rows = events.map((e) =>
					headers
						.map((h) => {
							const val = e[h as keyof AuditEvent] ?? "";
							const str =
								typeof val === "object" ? JSON.stringify(val) : String(val);
							return `"${str.replace(/"/g, '""')}"`;
						})
						.join(","),
				);
				data = [headers.join(","), ...rows].join("\n");
				mimeType = "text/csv";
				extension = "csv";
			}

			const blob = new Blob([data], { type: mimeType });
			const url = URL.createObjectURL(blob);
			const a = document.createElement("a");
			a.href = url;
			a.download = `audit-log-${new Date().toISOString().slice(0, 10)}.${extension}`;
			a.click();
			URL.revokeObjectURL(url);
		},
	});
}
