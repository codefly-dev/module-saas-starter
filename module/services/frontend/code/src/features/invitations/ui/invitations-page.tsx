"use client";

import { MailPlus } from "lucide-react";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import { hasPermission } from "@/lib/permissions";
import { readOutcome } from "@/shared/lib/read-outcome";
import {
	Button,
	ErrorState,
	Page as PageBody,
	PageHeader,
	Panel,
	Stack,
} from "@/shared/ui";
import { useInvitations } from "../service/queries";
import { InvitationForm } from "./invitation-form";
import { InvitationsTable } from "./invitations-table";

export function InvitationsPage() {
	const { organizationId: orgId = "", platformRole, orgRole } = useAuth();
	const canManage = hasPermission(
		platformRole,
		orgRole,
		PERMISSIONS.INVITATIONS_WRITE,
	);
	const {
		data: invitations = [],
		isLoading,
		isError,
		error,
		refetch,
	} = useInvitations(orgId || null);
	// A refusal is not cured by asking again, so only a read that did not come
	// back offers the retry.
	const refused = readOutcome(isError, error) === "forbidden";

	return (
		<PageBody>
			<PageHeader
				title="Invitations"
				description="Invite people to this organization and follow the invitations already sent."
				actions={
					<>
						{canManage && <InvitationForm key={orgId} orgId={orgId} />}
						<OrgSelector />
					</>
				}
			/>

			{!orgId ? (
				<EmptyState
					icon={MailPlus}
					title="Select an organization to view invitations"
					description="Each invitation belongs to one organization. Pick an organization from the selector above to see or send its invitations."
				/>
			) : isError ? (
				<Panel>
					<Stack gap={3} align="start">
						<ErrorState
							title="Couldn't load the invitations"
							detail={
								refused
									? "You don't have permission to see this organization's invitations. Ask one of its administrators for access."
									: "Whether any are pending is unknown. Try again; if it keeps failing, tell your administrator."
							}
						/>
						{!refused && (
							<Button variant="outline" size="sm" onClick={() => refetch()}>
								Try again
							</Button>
						)}
					</Stack>
				</Panel>
			) : (
				<InvitationsTable
					key={orgId}
					invitations={invitations}
					isLoading={isLoading}
					canManage={canManage}
				/>
			)}
		</PageBody>
	);
}
