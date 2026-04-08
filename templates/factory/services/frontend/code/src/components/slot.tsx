"use client";

import { Suspense } from "react";
import { useAdminConfig } from "@/lib/hooks/use-admin-config";

export function Slot({ name }: { name: string }) {
  const config = useAdminConfig();

  // For now, slot content comes from plugin widgets matching the slot name
  if (name === "dashboard.widgets") {
    return (
      <>
        {config.widgets.map((widget) => (
          <Suspense key={widget.id} fallback={<div className="animate-pulse bg-gray-200 dark:bg-gray-800 rounded-lg h-32" />}>
            <widget.component />
          </Suspense>
        ))}
      </>
    );
  }

  return null;
}
