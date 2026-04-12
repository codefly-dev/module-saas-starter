"use client";

import { useState } from "react";
import { useInvitations } from "../service/queries";
import { InvitationsTable } from "./invitations-table";
import { InvitationForm } from "./invitation-form";
import { OrgSelector } from "@/components/org-selector";
import type { Invitation } from "../model/types";

export function InvitationsPage() {
  const [orgId, setOrgId] = useState("");
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
          <OrgSelector value={orgId} onChange={setOrgId} />
        </div>
      </div>

      <InvitationsTable
        invitations={invitations as Invitation[]}
        isLoading={orgId ? isLoading : false}
      />
    </div>
  );
}
