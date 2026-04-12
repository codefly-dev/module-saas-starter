"use client";

import { useState } from "react";
import { useAPIKeys } from "../service/queries";
import { APIKeysTable } from "./api-keys-table";
import { APIKeyForm } from "./api-key-form";
import { OrgSelector } from "@/components/org-selector";
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

      <APIKeysTable
        keys={keys as APIKey[]}
        isLoading={orgId ? isLoading : false}
      />
    </div>
  );
}
