"use client";

import { useQuery } from "@tanstack/react-query";
import { X } from "lucide-react";
import { useAuth } from "@/lib/auth";
import { Badge, Button, Section } from "@/shared/ui";
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
		<Section
			aria-label={`Organization ${organization.name}`}
			className="mt-8"
			title={organization.name}
			description={
				<>
					<span className="font-mono">{organization.slug}</span>
					{organization.archived && (
						<>
							{" "}
							<Badge variant="outline">Archived</Badge>
						</>
					)}
				</>
			}
			actions={
				onClose ? (
					<Button
						variant="ghost"
						size="icon-sm"
						onClick={onClose}
						aria-label="Close organization"
					>
						<X />
					</Button>
				) : undefined
			}
		>
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
		</Section>
	);
}
