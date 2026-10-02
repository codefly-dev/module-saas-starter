"use client";

import { useState } from "react";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
} from "@/shared/ui";
import { DeleteOrganizationDialog } from "./delete-organization-dialog";
import { LeaveOrganizationDialog } from "./leave-organization-dialog";

interface OrganizationLifecycleCardProps {
	organization: { id: string; name: string; slug: string };
	canLeave: boolean;
	canDelete: boolean;
	onDeleted?: () => void;
}

/** Leave the organization, and the danger zone that deletes it. */
export function OrganizationLifecycleCard({
	organization,
	canLeave,
	canDelete,
	onDeleted,
}: OrganizationLifecycleCardProps) {
	const [leaving, setLeaving] = useState(false);
	const [deleting, setDeleting] = useState(false);
	if (!canLeave && !canDelete) return null;
	return (
		<Card className="border-destructive/40">
			<CardHeader>
				<CardTitle>Danger zone</CardTitle>
				<CardDescription>
					Leaving or deleting {organization.name} ends access to it.
				</CardDescription>
			</CardHeader>
			<CardContent className="space-y-4">
				{canLeave && (
					<div className="flex items-center justify-between gap-4">
						<div>
							<p className="font-medium">Leave organization</p>
							<p className="text-sm text-muted-foreground">
								Remove yourself from {organization.name}.
							</p>
						</div>
						<Button variant="outline" onClick={() => setLeaving(true)}>
							Leave
						</Button>
					</div>
				)}
				{canDelete && (
					<div className="flex items-center justify-between gap-4">
						<div>
							<p className="font-medium">Delete organization</p>
							<p className="text-sm text-muted-foreground">
								Remove every member and revoke its credentials. History is kept.
							</p>
						</div>
						<Button variant="destructive" onClick={() => setDeleting(true)}>
							Delete
						</Button>
					</div>
				)}
			</CardContent>
			<LeaveOrganizationDialog
				organization={organization}
				open={leaving}
				onOpenChange={setLeaving}
			/>
			<DeleteOrganizationDialog
				organization={organization}
				open={deleting}
				onOpenChange={setDeleting}
				onDeleted={onDeleted}
			/>
		</Card>
	);
}
