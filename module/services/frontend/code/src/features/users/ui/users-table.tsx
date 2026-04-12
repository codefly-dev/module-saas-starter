"use client";

import { useMemo } from "react";
import {
  createColumnHelper,
  getCoreRowModel,
  getSortedRowModel,
  getPaginationRowModel,
  useReactTable,
  type SortingState,
} from "@tanstack/react-table";
import { useState } from "react";
import { MoreHorizontal, ShieldOff, ShieldAlert, UserCog } from "lucide-react";
import { DataTable } from "@/shared/ui/data-table";
import {
  Badge,
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/shared/ui";
import { formatDate, truncateUUID } from "@/shared/lib/utils";
import type { User } from "../model/types";
import { toUserStatus } from "../model/types";
import { getStatusBadgeVariant, statusLabel, toDisplayName } from "../model/transforms";

const col = createColumnHelper<User>();

interface UsersTableProps {
  data: User[];
  isLoading: boolean;
  onSuspend: (user: User) => void;
  onUnsuspend: (user: User) => void;
  onImpersonate: (user: User) => void;
}

export function UsersTable({
  data,
  isLoading,
  onSuspend,
  onUnsuspend,
  onImpersonate,
}: UsersTableProps) {
  const [sorting, setSorting] = useState<SortingState>([]);

  const columns = useMemo(
    () => [
      col.accessor("primaryEmail", {
        header: "Email",
        cell: (info) => (
          <span className="font-medium">{info.getValue()}</span>
        ),
      }),
      col.display({
        id: "name",
        header: "Name",
        cell: ({ row }) =>
          toDisplayName(row.original.profile, row.original.primaryEmail),
      }),
      col.accessor("status", {
        header: "Status",
        cell: (info) => {
          const s = info.getValue();
          return (
            <Badge variant={getStatusBadgeVariant(s)}>
              {statusLabel(s)}
            </Badge>
          );
        },
      }),
      col.accessor("emailVerified", {
        header: "Verified",
        cell: (info) => (info.getValue() ? "Yes" : "No"),
      }),
      col.accessor("createdAt", {
        header: "Created",
        cell: (info) => (
          <span className="text-muted-foreground text-sm">
            {formatDate(info.getValue())}
          </span>
        ),
      }),
      col.accessor("uuid", {
        header: "ID",
        cell: (info) => (
          <span className="font-mono text-xs text-muted-foreground">
            {truncateUUID(info.getValue())}
          </span>
        ),
        enableSorting: false,
      }),
      col.display({
        id: "actions",
        cell: ({ row }) => {
          const user = row.original;
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
                {user.status === "active" && (
                  <DropdownMenuItem onClick={() => onSuspend(user)}>
                    <ShieldAlert className="mr-2 h-4 w-4" />
                    Suspend
                  </DropdownMenuItem>
                )}
                {user.status === "suspended" && (
                  <DropdownMenuItem onClick={() => onUnsuspend(user)}>
                    <ShieldOff className="mr-2 h-4 w-4" />
                    Unsuspend
                  </DropdownMenuItem>
                )}
                <DropdownMenuItem onClick={() => onImpersonate(user)}>
                  <UserCog className="mr-2 h-4 w-4" />
                  Impersonate
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          );
        },
      }),
    ],
    [onSuspend, onUnsuspend, onImpersonate],
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

  return <DataTable table={table} isLoading={isLoading} emptyMessage="No users found." />;
}
