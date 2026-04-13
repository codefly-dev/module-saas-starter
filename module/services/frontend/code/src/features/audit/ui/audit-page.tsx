"use client";

import { useState } from "react";
import { Download } from "lucide-react";
import { useAuditLog } from "../service/queries";
import { useExportAuditLog } from "../service/mutations";
import { AuditTable } from "./audit-table";
import { AUDIT_ACTION_TYPES } from "../model/types";
import type { AuditEvent } from "../model/types";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  Button,
} from "@/shared/ui";

export function AuditPage() {
  const [actionFilter, setActionFilter] = useState("all");
  const { data, isLoading } = useAuditLog({
    action: actionFilter === "all" ? undefined : actionFilter,
    pageSize: 100,
  });
  const exportMutation = useExportAuditLog();

  const handleExport = (format: "csv" | "json") => {
    exportMutation.mutate({
      format,
      action: actionFilter === "all" ? undefined : actionFilter,
    });
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Audit Log</h1>
          <p className="text-muted-foreground">
            View all system events and user activity.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button variant="outline" size="sm" disabled={exportMutation.isPending}>
                  <Download className="mr-2 h-4 w-4" />
                  {exportMutation.isPending ? "Exporting..." : "Export"}
                </Button>
              }
            />
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={() => handleExport("csv")}>
                Export as CSV
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => handleExport("json")}>
                Export as JSON
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
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
      </div>

      <AuditTable
        events={(data?.events ?? []) as AuditEvent[]}
        isLoading={isLoading}
      />
    </div>
  );
}
