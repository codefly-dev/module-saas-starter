"use client";

import { useState } from "react";
import { Key } from "lucide-react";
import { useAPIKeys } from "../service/queries";
import { APIKeysTable } from "./api-keys-table";
import { APIKeyForm } from "./api-key-form";
import { OrgSelector } from "@/components/org-selector";
import { EmptyState } from "@/components/empty-state";
import type { APIKey } from "../model/types";

export function APIKeysPage() {
  const [orgId, setOrgId] = useState("");
  const { data: keys = [], isLoading } = useAPIKeys(orgId || null);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">API Keys</h1>
          <p className="text-muted-foreground">
            Manage API keys for programmatic access.
          </p>
        </div>
        <div className="flex items-center gap-3">
          <APIKeyForm orgId={orgId} />
          <OrgSelector value={orgId} onChange={setOrgId} />
        </div>
      </div>

      {!orgId ? (
        <EmptyState
          icon={Key}
          title="Select an organization to view API keys"
          description="API keys are scoped to one tenant at a time. Pick an org from the selector above to see (or create) its keys."
        />
      ) : (
        <APIKeysTable keys={keys as APIKey[]} isLoading={isLoading} />
      )}
    </div>
  );
}
