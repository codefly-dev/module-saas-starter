"use client";

import { useState } from "react";
import { useAuditLog } from "../service/queries";
import { AuditTable } from "./audit-table";
import { AUDIT_ACTION_TYPES } from "../model/types";
import type { AuditEvent } from "../model/types";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/ui";

export function AuditPage() {
  const [actionFilter, setActionFilter] = useState("all");
  const { data, isLoading } = useAuditLog({
    action: actionFilter === "all" ? undefined : actionFilter,
    pageSize: 100,
  });

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Audit Log</h1>
          <p className="text-muted-foreground">
            View all system events and user activity.
          </p>
        </div>
        <Select value={actionFilter} onValueChange={(v) => { if (v) setActionFilter(v); }}>
          <SelectTrigger className="w-[220px]">
            <SelectValue placeholder="Filter by action" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All actions</SelectItem>
            {AUDIT_ACTION_TYPES.map((action) => (
              <SelectItem key={action} value={action}>
                {action}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <AuditTable
        events={(data?.events ?? []) as AuditEvent[]}
        isLoading={isLoading}
      />
    </div>
  );
}
