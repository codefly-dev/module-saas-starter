"use client";

import { X } from "lucide-react";
import { useAuth } from "@/lib/auth";
import { Badge, Button } from "@/shared/ui";
import { OrgMembersPanel } from "./org-members-panel";
import { OrganizationLifecycleCard } from "./organization-lifecycle-card";
import { OrganizationProfileForm } from "./organization-profile-form";

interface OrganizationDetailProps {
	organization: {
		id: string;
		name: string;
		slug: string;
		archived?: boolean;
	};
	/**
	 * "membership" is an organization's own administrators managing it;
	 * "platform" also allows reading the roster outside the current organization.
	 */
	source: "membership" | "platform";
	onClose?: () => void;
	onDeleted?: () => void;
}

/**
 * The platform directory can show a roster across organizations. Changes use
 * the active organization's signed role, and deletion requires its owner.
 */
export function OrganizationDetail({
	organization,
	source,
	onClose,
	onDeleted,
}: OrganizationDetailProps) {
	const { organizationId, orgRole, impersonation } = useAuth();
	const current = organizationId === organization.id;
	const canManage = current && !impersonation.isImpersonating && (orgRole === "admin" || orgRole === "owner");
	const canLeave = source === "membership" && current && !impersonation.isImpersonating;

	return (
		<section
			aria-label={`Organization ${organization.name}`}
			className="mt-8 space-y-6"
		>
			<div className="flex items-center justify-between">
				<div className="flex items-center gap-2">
					<h2 className="text-xl font-semibold">{organization.name}</h2>
					<span className="font-mono text-sm text-muted-foreground">
						{organization.slug}
					</span>
					{organization.archived && <Badge variant="outline">Archived</Badge>}
				</div>
				{onClose && (
					<Button
						variant="ghost"
						size="sm"
						onClick={onClose}
						aria-label="Close organization"
					>
						<X className="h-4 w-4" />
					</Button>
				)}
			</div>
			{organization.archived ? (
				<p className="text-sm text-muted-foreground">
					This organization was deleted. It has no members and admits no
					request; its history is kept.
				</p>
			) : (
				<>
					{canManage && <OrganizationProfileForm organization={organization} />}
					<OrgMembersPanel
						orgId={organization.id}
						orgName={organization.name}
						source={source}
					/>
					<OrganizationLifecycleCard
						organization={organization}
						canLeave={canLeave}
						canDelete={canManage && orgRole === "owner"}
						onDeleted={onDeleted}
					/>
				</>
			)}
		</section>
	);
}
