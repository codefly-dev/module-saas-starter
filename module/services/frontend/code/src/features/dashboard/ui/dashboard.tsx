"use client";

import { useAuth } from "@/lib/auth";
import type { DashboardDef } from "../model/schema";
import { MetricCard } from "./metric-card";

// Dashboard renders a declared data graph. Every metric reads the audit trail
// scoped to the viewer's organization by default; pass `orgId` to pin it.
export function Dashboard({
	data,
	orgId,
}: {
	data: DashboardDef;
	orgId?: string;
}) {
	const { organizationId } = useAuth();
	const resolvedOrgId = orgId ?? organizationId ?? "";

	return (
		<div className="space-y-4">
			{(data.title || data.description) && (
				<div className="space-y-1">
					{data.title && (
						<h2 className="text-lg font-semibold tracking-tight">
							{data.title}
						</h2>
					)}
					{data.description && (
						<p className="text-sm text-muted-foreground">{data.description}</p>
					)}
				</div>
			)}
			<div className="grid gap-4 md:grid-cols-2">
				{data.metrics.map((metric) => (
					<MetricCard
						key={metric.title}
						metric={metric}
						orgId={resolvedOrgId}
					/>
				))}
			</div>
		</div>
	);
}
