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
import { Badge } from "@/shared/ui";
import { formatDate, truncateUUID } from "@/shared/lib/utils";
import type { AuditEvent } from "../model/types";
import { formatAuditAction } from "../model/transforms";

const col = createColumnHelper<AuditEvent>();

export function AuditTable({
  events,
  isLoading,
}: {
  events: AuditEvent[];
  isLoading: boolean;
}) {
  const columns = useMemo(
    () => [
      col.accessor("createdAt", {
        header: "Time",
        cell: (info) => (
          <span className="whitespace-nowrap text-muted-foreground">
            {formatDate(info.getValue())}
          </span>
        ),
      }),
      col.accessor("action", {
        header: "Action",
        cell: (info) => (
          <Badge variant="outline">{formatAuditAction(info.getValue())}</Badge>
        ),
      }),
      col.accessor("actorId", {
        header: "Actor",
        cell: (info) => (
          <span className="font-mono text-xs text-muted-foreground">
            {truncateUUID(info.getValue())}
          </span>
        ),
      }),
      col.accessor("resource", {
        header: "Resource",
        cell: (info) => {
          const event = info.row.original;
          return (
            <span className="text-muted-foreground">
              {event.resource}/{truncateUUID(event.resourceId)}
            </span>
          );
        },
      }),
      col.accessor("ipAddress", {
        header: "IP Address",
        cell: (info) => (
          <span className="font-mono text-xs text-muted-foreground">
            {info.getValue() || "-"}
          </span>
        ),
      }),
    ],
    [],
  );

  const table = useReactTable({
    data: events,
    columns,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
  });

  return (
    <DataTable
      table={table}
      isLoading={isLoading}
      emptyMessage="No audit events"
    />
  );
}
