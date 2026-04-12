"use client";

import { useMemo, useState } from "react";
import {
  createColumnHelper,
  getCoreRowModel,
  useReactTable,
} from "@tanstack/react-table";
import { Trash2 } from "lucide-react";
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
import type { MFADevice } from "../model/types";
import { formatDeviceType } from "../model/transforms";

const col = createColumnHelper<MFADevice>();

interface MFADevicesProps {
  data: MFADevice[];
  isLoading: boolean;
  onRevoke: (device: MFADevice) => void;
}

export function MFADevices({ data, isLoading, onRevoke }: MFADevicesProps) {
  const columns = useMemo(
    () => [
      col.accessor("name", {
        header: "Name",
        cell: (info) => (
          <span className="font-medium">{info.getValue()}</span>
        ),
      }),
      col.accessor("type", {
        header: "Type",
        cell: (info) => (
          <Badge variant="secondary">{formatDeviceType(info.getValue())}</Badge>
        ),
      }),
      col.accessor("createdAt", {
        header: "Added",
        cell: (info) => (
          <span className="text-muted-foreground text-sm">
            {formatDate(info.getValue())}
          </span>
        ),
      }),
      col.accessor("lastUsedAt", {
        header: "Last Used",
        cell: (info) => (
          <span className="text-muted-foreground text-sm">
            {formatDate(info.getValue())}
          </span>
        ),
      }),
      col.display({
        id: "actions",
        cell: ({ row }) => {
          const device = row.original;
          return (
            <AlertDialog>
              <AlertDialogTrigger
                render={
                  <Button variant="ghost" size="sm" className="h-8 w-8 p-0">
                    <Trash2 className="h-4 w-4" />
                  </Button>
                }
              />
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>Revoke device?</AlertDialogTitle>
                  <AlertDialogDescription>
                    This will remove &quot;{device.name}&quot; from your MFA
                    devices. You will need to re-enroll if you want to use it
                    again.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Cancel</AlertDialogCancel>
                  <AlertDialogAction
                    onClick={() => onRevoke(device)}
                    className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
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
    [onRevoke],
  );

  const table = useReactTable({
    data,
    columns,
    getCoreRowModel: getCoreRowModel(),
  });

  return (
    <DataTable
      table={table}
      isLoading={isLoading}
      emptyMessage="No MFA devices enrolled."
    />
  );
}
