"use client";

/**
 * RateLimitBanner — slim sticky warning surfaced when the
 * authenticated client has burned >90% of its rate-limit budget.
 * Subscribes to the rate-limit tracker so it updates the moment the
 * server returns headers; auto-hides when the window resets.
 *
 * Why a banner not a toast: this is a sustained state ("you are
 * close to the limit, slow down or upgrade") not an event. A toast
 * would dismiss too quickly. The banner only appears at >90% so it
 * doesn't pester users in normal use.
 */

import { useEffect, useState } from "react";
import { AlertTriangle } from "lucide-react";
import {
  getRateLimit,
  subscribeRateLimit,
  type RateLimitSnapshot,
} from "@/lib/connect/rate-limit-tracker";

const WARN_RATIO = 0.1; // banner kicks in when remaining/limit < 10%

export function RateLimitBanner() {
  const [snap, setSnap] = useState<RateLimitSnapshot | null>(getRateLimit());

  useEffect(() => {
    return subscribeRateLimit(setSnap);
  }, []);

  // Auto-clear once the window resets — without this the banner
  // would linger after the user stopped firing requests until the
  // next call repopulates the snapshot.
  useEffect(() => {
    if (!snap) return;
    const msUntilReset = snap.resetAt * 1000 - Date.now();
    if (msUntilReset <= 0) return;
    const t = window.setTimeout(() => setSnap(null), msUntilReset);
    return () => window.clearTimeout(t);
  }, [snap]);

  if (!snap) return null;
  if (snap.remaining / snap.limit >= WARN_RATIO) return null;

  const seconds = Math.max(0, snap.resetAt - Math.floor(Date.now() / 1000));

  return (
    <div className="fixed bottom-4 left-1/2 -translate-x-1/2 z-50 flex items-center gap-2 rounded-full border border-amber-500/40 bg-amber-500/10 backdrop-blur px-4 py-2 text-xs font-medium text-amber-900 dark:text-amber-200 shadow-lg animate-in fade-in slide-in-from-bottom-2 duration-200">
      <AlertTriangle className="h-3.5 w-3.5" />
      <span>
        Rate limit running low — {snap.remaining} of {snap.limit} requests
        left. Resets in {seconds}s.
      </span>
    </div>
  );
}
