"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	AlertTriangle,
	CheckCircle2,
	HardDriveDownload,
	Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { useAuth } from "@/lib/auth";
import { formatDate } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Card,
	CardContent,
	CardHeader,
	CardTitle,
	Skeleton,
} from "@/shared/ui";
import type { AuditExportFormValues } from "../model/schemas";
import { auditExportMutations } from "../service/mutations";
import { auditExportQueries } from "../service/queries";
import { AuditExportForm } from "./audit-export-form";

/**
 * AuditExportPage — per-org config for streaming the audit log to a
 * customer S3 bucket (or any S3-compatible endpoint). Shows the
 * exporter's last-run state inline so an operator can tell at a
 * glance whether things are working.
 *
 * State surfaces:
 *   - `lastExportedAt` populated + no error  → green "Last exported …"
 *   - `lastError` populated                  → red banner with message
 *   - both empty                             → blue "Not yet exported"
 */
export function AuditExportPage() {
	const queryClient = useQueryClient();
	const { organizationId: orgId = "" } = useAuth();

	const { data: cfg, isLoading } = useQuery(auditExportQueries.config(orgId));

	const save = useMutation({
		mutationFn: (values: AuditExportFormValues) =>
			auditExportMutations.save(orgId, values),
		onSuccess: () => {
			toast.success("Audit export saved");
			void queryClient.invalidateQueries({
				queryKey: ["audit-export", "config", orgId],
			});
		},
		onError: (err) => toast.error("Save failed", { description: err.message }),
	});

	const remove = useMutation({
		mutationFn: () => auditExportMutations.delete(orgId),
		onSuccess: () => {
			toast.success("Audit export disabled");
			void queryClient.invalidateQueries({
				queryKey: ["audit-export", "config", orgId],
			});
		},
		onError: (err) =>
			toast.error("Delete failed", { description: err.message }),
	});

	function handleDelete() {
		const ok = window.confirm(
			"Stop exporting audit logs to S3 for this org?\n\n" +
				"Stored credentials will be deleted. The exporter will stop on " +
				"its next tick. You'll need to re-enter the bucket + keys to " +
				"resume.",
		);
		if (ok) remove.mutate();
	}

	// Form initial values: only fill from cfg when present. The api
	// returns an empty secretAccessKey on Get, which is exactly what
	// the form expects (blank input → preserve on submit).
	const initial: AuditExportFormValues | undefined = cfg
		? {
				bucket: cfg.bucket,
				region: cfg.region,
				endpoint: cfg.endpoint,
				prefix: cfg.prefix,
				accessKeyId: cfg.accessKeyId,
				secretAccessKey: "",
				cadenceMinutes: cfg.cadenceMinutes || 60,
				enabled: cfg.enabled,
			}
		: undefined;

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<div>
					<h1 className="text-2xl font-bold tracking-tight">Audit Export</h1>
					<p className="text-muted-foreground">
						Stream your audit log to S3 (or any S3-compatible store) on a
						schedule. Compliance teams get a tamper-evident copy outside the
						platform.
					</p>
				</div>
				<OrgSelector />
			</div>

			{!orgId ? (
				<EmptyState
					icon={HardDriveDownload}
					title="Select an organization"
					description="Audit-export configs are scoped to one tenant at a time. Pick an org above to view or edit its export settings."
				/>
			) : isLoading ? (
				<div className="space-y-3">
					<Skeleton className="h-32 w-full" />
					<Skeleton className="h-64 w-full" />
				</div>
			) : (
				<div className="space-y-6">
					<ExporterStatus cfg={cfg} />

					<Card>
						<CardHeader>
							<CardTitle className="text-base">
								{cfg ? "Update export settings" : "Configure exports"}
							</CardTitle>
						</CardHeader>
						<CardContent>
							<AuditExportForm
								key={orgId}
								initial={initial}
								isPending={save.isPending}
								onSubmit={(values) => save.mutate(values)}
							/>
						</CardContent>
					</Card>

					{cfg && (
						<div className="flex justify-end">
							<Button
								variant="outline"
								size="sm"
								onClick={handleDelete}
								disabled={remove.isPending}
								className="text-destructive border-destructive/30 hover:bg-destructive/10"
							>
								<Trash2 className="mr-2 h-4 w-4" />
								{remove.isPending ? "Removing…" : "Remove configuration"}
							</Button>
						</div>
					)}
				</div>
			)}
		</div>
	);
}

interface StatusCfg {
	enabled: boolean;
	lastExportedAt?: { seconds: bigint };
	lastError: string;
	lastErrorAt?: { seconds: bigint };
	cadenceMinutes: number;
}

/**
 * ExporterStatus — last-run summary for the configured exporter.
 * Shows nothing when no config exists (the form below is the
 * configure prompt). Three visual states:
 *   - error: red banner with `lastError`
 *   - success: green banner with relative-time of `lastExportedAt`
 *   - never-run: blue "Configured but not yet exported"
 */
function ExporterStatus({ cfg }: { cfg?: StatusCfg | null }) {
	if (!cfg) return null;
	const tsToISO = (t?: { seconds: bigint }) =>
		t ? new Date(Number(t.seconds) * 1000).toISOString() : undefined;

	if (cfg.lastError) {
		return (
			<div className="rounded-md border border-destructive/30 bg-destructive/5 p-4 flex items-start gap-3">
				<AlertTriangle className="h-5 w-5 text-destructive shrink-0 mt-0.5" />
				<div className="space-y-1">
					<div className="font-medium text-sm">Last export failed</div>
					<div className="text-sm text-destructive/90 font-mono break-all">
						{cfg.lastError}
					</div>
					<div className="text-xs text-muted-foreground">
						At {formatDate(tsToISO(cfg.lastErrorAt))} • Next attempt in{" "}
						{cfg.cadenceMinutes} min
					</div>
				</div>
			</div>
		);
	}

	if (cfg.lastExportedAt) {
		return (
			<div className="rounded-md border border-emerald-500/30 bg-emerald-500/5 p-4 flex items-start gap-3">
				<CheckCircle2 className="h-5 w-5 text-emerald-600 dark:text-emerald-400 shrink-0 mt-0.5" />
				<div className="space-y-1">
					<div className="font-medium text-sm">
						Last exported {formatDate(tsToISO(cfg.lastExportedAt))}
					</div>
					<div className="text-xs text-muted-foreground">
						Cadence: every {cfg.cadenceMinutes} min •{" "}
						<Badge variant={cfg.enabled ? "default" : "secondary"}>
							{cfg.enabled ? "Enabled" : "Disabled"}
						</Badge>
					</div>
				</div>
			</div>
		);
	}

	return (
		<div className="rounded-md border bg-muted/30 p-4 text-sm">
			<div className="font-medium">Configured but not yet exported</div>
			<div className="text-xs text-muted-foreground mt-1">
				The exporter runs every {cfg.cadenceMinutes} min and will pick this
				config up on its next tick. Disable + re-enable, or wait, to trigger.
			</div>
		</div>
	);
}
