"use client";

import { useQuery } from "@tanstack/react-query";
import { X } from "lucide-react";
import { useAuth } from "@/lib/auth";
import { Badge, Button } from "@/shared/ui";
import { toOrgRole } from "../model/types";
import { orgQueries } from "../service/queries";
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
	 * "platform" is a platform administrator managing any organization.
	 */
	source: "membership" | "platform";
	onClose?: () => void;
	onDeleted?: () => void;
}

/**
 * One organization, managed by hand: its name and slug, its members and their
 * roles, and leaving or deleting it. The same surface serves an organization's
 * administrators and a platform administrator; only the roster's source and
 * who may leave differ. The server authorizes every write — the controls shown
 * here follow what it will allow, and its refusals are shown as it words them.
 */
export function OrganizationDetail({
	organization,
	source,
	onClose,
	onDeleted,
}: OrganizationDetailProps) {
	const { user, platformRole } = useAuth();
	// The caller's own role decides whether "Delete" is theirs to offer; it is
	// read from the same roster the members table shows.
	const { data: roster } = useQuery({
		...orgQueries.members(organization.id),
		enabled: source === "membership" && !organization.archived,
	});
	const callerRole = roster?.members.find((m) => m.userId === user?.id)?.role;
	const isOwner =
		callerRole !== undefined &&
		toOrgRole(callerRole as unknown as number) === "owner";
	const canDelete =
		!organization.archived && (platformRole === "super_admin" || isOwner);
	const canLeave = source === "membership" && !organization.archived;

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
					<OrganizationProfileForm organization={organization} />
					<OrgMembersPanel
						orgId={organization.id}
						orgName={organization.name}
						source={source}
					/>
					<OrganizationLifecycleCard
						organization={organization}
						canLeave={canLeave}
						canDelete={canDelete}
						onDeleted={onDeleted}
					/>
				</>
			)}
		</section>
	);
}
