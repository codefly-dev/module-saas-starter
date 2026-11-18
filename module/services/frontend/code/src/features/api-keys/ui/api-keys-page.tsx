"use client";

import { Key } from "lucide-react";
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
import type { APIKey } from "../model/types";
import { useAPIKeys } from "../service/queries";
import { APIKeyForm } from "./api-key-form";
import { APIKeysTable } from "./api-keys-table";

export function APIKeysPage() {
	const { organizationId: orgId = "", platformRole, orgRole } = useAuth();
	const canManage = hasPermission(
		platformRole,
		orgRole,
		PERMISSIONS.API_KEYS_WRITE,
	);
	const {
		data: keys = [],
		isLoading,
		isError,
		error,
		refetch,
	} = useAPIKeys(orgId || null);
	// A refusal is not cured by asking again, so only a read that did not come
	// back offers the retry.
	const refused = readOutcome(isError, error) === "forbidden";

	return (
		<PageBody>
			<PageHeader
				title="API keys"
				description="Create and revoke the keys other programs use to reach this organization."
				actions={
					<>
						{canManage && <APIKeyForm key={orgId} orgId={orgId} />}
						<OrgSelector />
					</>
				}
			/>

			{!orgId ? (
				<EmptyState
					icon={Key}
					title="Select an organization to view API keys"
					description="Each key belongs to one organization. Pick an organization from the selector above to see or create its keys."
				/>
			) : isError ? (
				<Panel>
					<Stack gap={3} align="start">
						<ErrorState
							title="Couldn't load the API keys"
							detail={
								refused
									? "You don't have permission to see this organization's API keys. Ask one of its administrators for access."
									: "Whether this organization has any is unknown. Try again; if it keeps failing, tell your administrator."
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
				<APIKeysTable
					key={orgId}
					keys={keys as APIKey[]}
					isLoading={isLoading}
					canManage={canManage}
				/>
			)}
		</PageBody>
	);
}
