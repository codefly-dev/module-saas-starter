"use client";

import { OrgSelector } from "@/components/org-selector";
import { useAuth } from "@/lib/auth";
import type { Invitation } from "../model/types";
import { useInvitations } from "../service/queries";
import { InvitationForm } from "./invitation-form";
import { InvitationsTable } from "./invitations-table";

export function InvitationsPage() {
	const { organizationId: orgId = "" } = useAuth();
	const { data: invitations = [], isLoading } = useInvitations(orgId || null);

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<div>
					<h1 className="text-2xl font-bold tracking-tight">Invitations</h1>
					<p className="text-muted-foreground">
						Manage organization invitations.
					</p>
				</div>
				<div className="flex items-center gap-3">
					<InvitationForm orgId={orgId} />
					<OrgSelector />
				</div>
			</div>

			<InvitationsTable
				invitations={invitations as Invitation[]}
				isLoading={orgId ? isLoading : false}
			/>
		</div>
	);
}
