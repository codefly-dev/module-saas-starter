"use client";

import { useMemo } from "react";
import {
  createColumnHelper,
  getCoreRowModel,
  getSortedRowModel,
  getPaginationRowModel,
  useReactTable,
} from "@tanstack/react-table";
import { Ban } from "lucide-react";
import { toast } from "sonner";
import { DataTable } from "@/shared/ui/data-table";
import {
  Badge,
  Button,
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/shared/ui";
import { formatDate } from "@/shared/lib/utils";
import type { APIKey } from "../model/types";
import { formatEnvironment, maskKeyPrefix } from "../model/transforms";
import { useRevokeAPIKey } from "../service/mutations";

const col = createColumnHelper<APIKey>();

export function APIKeysTable({
  keys,
  isLoading,
}: {
  keys: APIKey[];
  isLoading: boolean;
}) {
  const revokeKey = useRevokeAPIKey();

  const columns = useMemo(
    () => [
      col.accessor("name", { header: "Name" }),
      col.accessor("prefix", {
        header: "Key",
        cell: (info) => (
          <code className="rounded bg-muted px-2 py-1 text-xs font-mono">
            {maskKeyPrefix(info.getValue())}
          </code>
        ),
      }),
      col.accessor("environment", {
        header: "Environment",
        cell: (info) => {
          const env = formatEnvironment(info.getValue());
          return (
            <Badge variant={env === "live" ? "default" : "secondary"}>
              {env}
            </Badge>
          );
        },
      }),
      col.accessor("lastUsedAt", {
        header: "Last Used",
        cell: (info) => (
          <span className="text-muted-foreground">
            {formatDate(info.getValue())}
          </span>
        ),
      }),
      col.accessor("createdAt", {
        header: "Created",
        cell: (info) => (
          <span className="text-muted-foreground">
            {formatDate(info.getValue())}
          </span>
        ),
      }),
      col.display({
        id: "actions",
        header: "",
        cell: ({ row }) => {
          const key = row.original;
          if (key.revokedAt) {
            return (
              <Badge variant="destructive" className="text-xs">
                Revoked
              </Badge>
            );
          }
          return (
            <AlertDialog>
              <AlertDialogTrigger
                render={<Button variant="ghost" size="sm" />}
              >
                <Ban className="h-4 w-4 text-destructive" />
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>Revoke API key</AlertDialogTitle>
                  <AlertDialogDescription>
                    This will permanently revoke the key &quot;{key.name}&quot;.
                    Any applications using this key will stop working
                    immediately.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Cancel</AlertDialogCancel>
                  <AlertDialogAction
                    onClick={() =>
                      revokeKey.mutate(key.id, {
                        onSuccess: () =>
                          toast.success(`Key "${key.name}" revoked`),
                        onError: () => toast.error("Failed to revoke key"),
                      })
                    }
                  >
                    Revoke
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          );
        },
      }),
    ],
    [revokeKey],
  );

  const table = useReactTable({
    data: keys,
    columns,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
  });

  return (
    <DataTable
      table={table}
      isLoading={isLoading}
      emptyMessage="No API keys"
    />
  );
}
