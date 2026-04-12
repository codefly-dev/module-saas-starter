"use client";

import { useMemo, useState } from "react";
import {
  createColumnHelper,
  getCoreRowModel,
  getSortedRowModel,
  getPaginationRowModel,
  useReactTable,
  type SortingState,
} from "@tanstack/react-table";
import { MoreHorizontal, Users } from "lucide-react";
import { DataTable } from "@/shared/ui/data-table";
import {
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/shared/ui";
import { formatDate, truncateUUID } from "@/shared/lib/utils";
import type { Team } from "../model/types";

const col = createColumnHelper<Team>();

interface TeamsTableProps {
  data: Team[];
  isLoading: boolean;
  onViewMembers: (team: Team) => void;
}

export function TeamsTable({
  data,
  isLoading,
  onViewMembers,
}: TeamsTableProps) {
  const [sorting, setSorting] = useState<SortingState>([]);

  const columns = useMemo(
    () => [
      col.accessor("name", {
        header: "Name",
        cell: (info) => (
          <span className="font-medium">{info.getValue()}</span>
        ),
      }),
      col.accessor("description", {
        header: "Description",
        cell: (info) => (
          <span className="text-sm text-muted-foreground">
            {info.getValue() || "-"}
          </span>
        ),
      }),
      col.accessor("id", {
        header: "ID",
        cell: (info) => (
          <span className="font-mono text-xs text-muted-foreground">
            {truncateUUID(info.getValue())}
          </span>
        ),
        enableSorting: false,
      }),
      col.accessor("createdAt", {
        header: "Created",
        cell: (info) => (
          <span className="text-sm text-muted-foreground">
            {formatDate(info.getValue())}
          </span>
        ),
      }),
      col.display({
        id: "actions",
        cell: ({ row }) => {
          const team = row.original;
          return (
            <DropdownMenu>
              <DropdownMenuTrigger
                render={<Button variant="ghost" size="sm" className="h-8 w-8 p-0" />}
              >
                <MoreHorizontal className="h-4 w-4" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuLabel>Actions</DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuItem onClick={() => onViewMembers(team)}>
                  <Users className="mr-2 h-4 w-4" />
                  View Members
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          );
        },
      }),
    ],
    [onViewMembers],
  );

  const table = useReactTable({
    data,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
  });

  return (
    <DataTable
      table={table}
      isLoading={isLoading}
      emptyMessage="No teams yet."
    />
  );
}
