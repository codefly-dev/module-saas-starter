"use client";

import {
	DatasourceAccountLinks,
	DatasourceDirectoryPanel,
	DatasourcesPanel,
	DatasourceSyncSchedule,
} from "@codefly-dev/saas-ui";
import { Disclosure } from "@codefly-dev/ui/layout";
import { HardDriveDownload } from "lucide-react";
import { type ReactNode, useMemo } from "react";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { useAuth } from "@/lib/auth";
import { usePublicRuntimeConfig } from "@/lib/public-runtime-config-provider";
import { createDatasourceClient } from "./datasource-client";

/**
 * Admin → Data sources: the organization's sources first, then how to connect
 * another, each with who can read it; what most organizations never change
 * waits, collapsed, under Advanced.
 *
 * The declared reconcile trigger behind each schedule — its cron, operation
 * and identity — is an operator's view and lives on Platform → Unattended
 * work; here a schedule is one plain sentence per source.
 */
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
		<div className="space-y-8">
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div className="space-y-1">
					<h2 data-slot="page-title" className="type-page-title">
						Data sources
					</h2>
					<p className="text-muted-foreground">
						Bring content in from the tools your organization uses, and choose
						who can read it.
					</p>
				</div>
				<OrgSelector />
			</div>

			{orgId ? (
				<>
					<DatasourcesPanel
						key={orgId}
						client={client}
						orgId={orgId}
						canManage={canManage}
					/>
					<Disclosure title="Advanced" key={`advanced-${orgId}`}>
						<div className="space-y-8 pt-2">
							<p className="type-body text-muted-foreground">
								Settings most organizations never need to change.
							</p>
							<AdvancedSection
								title="Sync schedule"
								description="How often each source refreshes on its own."
							>
								<DatasourceSyncSchedule client={client} orgId={orgId} />
							</AdvancedSection>
							{/* Who a shared item is readable by: the explicit mappings a
							    source's per-item access lists are translated through. */}
							<DatasourceAccountLinks client={client} orgId={orgId} />
							{canManage && (
								<DatasourceDirectoryPanel client={client} orgId={orgId} />
							)}
						</div>
					</Disclosure>
				</>
			) : (
				<EmptyState
					icon={HardDriveDownload}
					title="Select an organization to see its data sources"
					description="Pick an organization from the selector above."
				/>
			)}
		</div>
	);
}

function AdvancedSection({
	title,
	description,
	children,
}: {
	title: string;
	description: string;
	children: ReactNode;
}) {
	return (
		<section aria-label={title} className="space-y-2">
			<h4 className="type-label">{title}</h4>
			<p className="type-caption-plain text-muted-foreground">{description}</p>
			{children}
		</section>
	);
}
