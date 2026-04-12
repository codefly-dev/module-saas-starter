"use client";

import { useState } from "react";
import { useAuth } from "@/lib/auth";

export function ImpersonationBanner() {
  const { impersonation, user, logout } = useAuth();
  const [minimized, setMinimized] = useState(false);

  if (!impersonation.isImpersonating) return null;

  if (minimized) {
    return (
      <button
        onClick={() => setMinimized(false)}
        className="fixed bottom-4 right-4 z-[9999] bg-amber-400 text-amber-900 px-3 py-1.5 rounded-full text-xs font-bold shadow-lg hover:bg-amber-300 transition-colors"
        aria-label="Expand impersonation banner"
      >
        Impersonating
      </button>
    );
  }

  return (
    <div className="fixed bottom-0 left-0 right-0 z-[9999] bg-amber-400 text-amber-900 border-t-2 border-amber-500">
      <div className="flex items-center justify-between px-4 py-2 max-w-screen-xl mx-auto">
        <div className="flex items-center gap-2 text-sm">
          <span className="font-bold">Impersonation Active</span>
          <span>&mdash;</span>
          <span>
            Viewing as <strong>{user?.email || user?.id}</strong>
          </span>
          {impersonation.impersonatorId && (
            <span className="text-amber-700">
              (by {impersonation.impersonatorId})
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => setMinimized(true)}
            className="text-amber-700 hover:text-amber-900 text-xs underline"
          >
            Minimize
          </button>
          <button
            onClick={logout}
            className="bg-amber-900 text-amber-100 px-3 py-1 rounded text-sm font-medium hover:bg-amber-800 transition-colors"
          >
            Stop Impersonating
          </button>
        </div>
      </div>
    </div>
  );
}
