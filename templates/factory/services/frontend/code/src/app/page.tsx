"use client";

import { Slot } from "@/components/slot";

export default function DashboardPage() {
  return (
    <div>
      <h2 className="text-2xl font-bold mb-6">Dashboard</h2>
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
        <Slot name="dashboard.widgets" />
      </div>
    </div>
  );
}
