"use client";

import { useMemo } from "react";
import {
  createColumnHelper,
  getCoreRowModel,
  getSortedRowModel,
  getPaginationRowModel,
  useReactTable,
} from "@tanstack/react-table";
import { DataTable } from "@/shared/ui/data-table";
import { formatDate, truncateUUID } from "@/shared/lib/utils";
import type { SessionInfo } from "../model/types";
import { useActiveSessions } from "../service/queries";

const col = createColumnHelper<SessionInfo>();

export function SessionsPage() {
  const { data: sessions = [], isLoading } = useActiveSessions();

  const columns = useMemo(
    () => [
      col.accessor("userId", {
        header: "User",
        cell: (info) => (
          <span className="font-mono text-xs">
            {truncateUUID(info.getValue())}
          </span>
        ),
      }),
      col.accessor("ipAddress", {
        header: "IP Address",
        cell: (info) => (
          <span className="font-mono text-xs">{info.getValue() || "-"}</span>
        ),
      }),
      col.accessor("deviceInfo", {
        header: "Device",
        cell: (info) => {
          const device = info.getValue();
          if (!device || Object.keys(device).length === 0) {
            return <span className="text-muted-foreground">-</span>;
          }
          return (
            <span className="text-xs text-muted-foreground">
              {device.browser || device.os || Object.values(device)[0] || "-"}
            </span>
          );
        },
      }),
      col.accessor("lastActiveAt", {
        header: "Last Active",
        cell: (info) => (
          <span className="text-muted-foreground">
            {formatDate(info.getValue())}
          </span>
        ),
      }),
      col.accessor("expiresAt", {
        header: "Expires",
        cell: (info) => (
          <span className="text-muted-foreground">
            {formatDate(info.getValue())}
          </span>
        ),
      }),
    ],
    [],
  );

  const table = useReactTable({
    data: sessions as SessionInfo[],
    columns,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
  });

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Active Sessions</h1>
        <p className="text-muted-foreground">
          View and monitor active user sessions.
        </p>
      </div>

      <DataTable
        table={table}
        isLoading={isLoading}
        emptyMessage="No active sessions"
      />
    </div>
  );
}
