"use client";

import {
	DatasourceAccountLinks,
	DatasourceDirectoryPanel,
	DatasourcesPanel,
} from "@codefly-dev/saas-ui";
import { HardDriveDownload } from "lucide-react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { useMemo } from "react";
import { useAuth } from "@/lib/auth";
import { usePublicRuntimeConfig } from "@/lib/public-runtime-config-provider";
import { createDatasourceClient } from "./datasource-client";

export function DatasourcesAdmin() {
	const { organizationId: orgId = "", orgRole, platformRole } = useAuth();
	// The tier DatasourceService requires to connect, sync or remove a source and
	// to grant read access. Other admin roles (support, billing) reach this page
	// but would be refused, so the panel does not offer them those controls.
	const canManage =
		orgRole === "owner" ||
		orgRole === "admin" ||
		platformRole === "super_admin";
	const { collectionContentResource } = usePublicRuntimeConfig();
	const client = useMemo(
		() => createDatasourceClient(collectionContentResource),
		[collectionContentResource],
	);

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<h2 data-slot="page-title" className="type-page-title">Data sources</h2>
				<OrgSelector />
			</div>

			{orgId ? (
				<>
					<DatasourcesPanel
						key={orgId}
						client={client}
						orgId={orgId}
						canManage={canManage}
						onSyncEnqueued={(jobId) =>
							toast.success("Sync enqueued", { description: `Job ${jobId}` })
						}
					/>
					{/* Who a shared item is readable by: the explicit mappings a
					    source's per-item access lists are translated through. */}
					<DatasourceAccountLinks key={`links-${orgId}`} client={client} orgId={orgId} />
					<DatasourceDirectoryPanel key={`directory-${orgId}`} client={client} orgId={orgId} />
				</>
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
