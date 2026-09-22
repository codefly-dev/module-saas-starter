"use client";

import { Key } from "lucide-react";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { useAuth } from "@/lib/auth";
import type { APIKey } from "../model/types";
import { useAPIKeys } from "../service/queries";
import { APIKeyForm } from "./api-key-form";
import { APIKeysTable } from "./api-keys-table";

export function APIKeysPage() {
	const { organizationId: orgId = "" } = useAuth();
	const {
		data: keys = [],
		isLoading,
		isError,
		refetch,
	} = useAPIKeys(orgId || null);

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<div>
					<h1 data-slot="page-title" className="type-page-title">
						API Keys
					</h1>
					<p className="text-muted-foreground">
						Manage API keys for programmatic access.
					</p>
				</div>
				<div className="flex items-center gap-3">
					<APIKeyForm key={orgId} orgId={orgId} />
					<OrgSelector />
				</div>
			</div>

			{!orgId ? (
				<EmptyState
					icon={Key}
					title="Select an organization to view API keys"
					description="API keys are scoped to one tenant at a time. Pick an org from the selector above to see (or create) its keys."
				/>
			) : isError ? (
				<div role="alert">
					Couldn&apos;t load API keys.{" "}
					<button type="button" className="underline" onClick={() => refetch()}>
						Try again
					</button>
				</div>
			) : (
				<APIKeysTable keys={keys as APIKey[]} isLoading={isLoading} />
			)}
		</div>
	);
}
