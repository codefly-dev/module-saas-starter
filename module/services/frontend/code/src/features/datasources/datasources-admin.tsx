"use client";

import { DatasourcesPanel } from "@codefly/saas-ui";
import { HardDriveDownload } from "lucide-react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { useAuth } from "@/lib/auth";
import { datasourceClient } from "./datasource-client";

export function DatasourcesAdmin() {
	const { organizationId: orgId = "" } = useAuth();

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<h2 className="text-2xl font-bold tracking-tight">Data sources</h2>
				<OrgSelector />
			</div>

			{orgId ? (
				<DatasourcesPanel
					key={orgId}
					client={datasourceClient}
					orgId={orgId}
					onSyncEnqueued={(jobId) =>
						toast.success("Sync enqueued", { description: `Job ${jobId}` })
					}
				/>
			) : (
				<EmptyState
					icon={HardDriveDownload}
					title="Select an organization to view data sources"
					description="Data sources are scoped to one tenant at a time. Pick an org from the selector above to see (or connect) its sources."
				/>
			)}
		</div>
	);
}
