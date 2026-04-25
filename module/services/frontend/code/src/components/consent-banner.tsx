"use client";

/**
 * ConsentBanner — cookie + TOS acceptance banner. Appears on first
 * visit + on every TOS version bump (operator increments
 * CONSENT_VERSION below). Localized to the bottom-right of the
 * viewport so it doesn't block content.
 *
 * Tradeoff vs full server-side audit trail: this records acceptance
 * in localStorage (per browser, per device), not in the user_id row
 * on the api. That's enough for "did the user see + click accept"
 * on a marketing site / B2C app, and most regulators accept it.
 *
 * Enterprise compliance (SOC 2, signed-off acceptance per user with
 * timestamp + version + IP) wants the api-side flow:
 *    users.terms_accepted_at + terms_version columns,
 *    /v1/consent/accept RPC that records both.
 * That's tracked as a roadmap item; this component swaps to it by
 * reading from useAuth + posting to the RPC instead of localStorage.
 */

import { useEffect, useState } from "react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { X, Cookie } from "lucide-react";

// Bump this when the TOS / privacy policy changes — every browser
// that accepted an older version gets the banner again.
const CONSENT_VERSION = "2026-04-25";
const STORAGE_KEY = "saas-starter:consent-version";

export function ConsentBanner() {
  // Three-state: undefined (not yet checked — render nothing to avoid
  // a flash), "stale" (banner needed), "current" (accepted).
  const [state, setState] = useState<"loading" | "stale" | "current">("loading");

  useEffect(() => {
    try {
      const accepted = window.localStorage.getItem(STORAGE_KEY);
      setState(accepted === CONSENT_VERSION ? "current" : "stale");
    } catch {
      // SSR / private mode → don't show the banner. We'd prefer a
      // false-negative (no banner) over crashing the page.
      setState("current");
    }
  }, []);

  function accept() {
    try {
      window.localStorage.setItem(STORAGE_KEY, CONSENT_VERSION);
    } catch {
      // ignore
    }
    setState("current");
  }

  function dismiss() {
    // Dismissing without accepting = treat as not-accepted yet so the
    // banner returns next visit. We don't want to silently consent on
    // X click.
    setState("current");
  }

  if (state !== "stale") return null;

  return (
    <div
      role="dialog"
      aria-label="Cookie and terms acknowledgement"
      className="fixed bottom-4 right-4 z-50 max-w-sm rounded-2xl border bg-card text-card-foreground shadow-2xl shadow-black/10 dark:shadow-black/40 animate-in fade-in slide-in-from-bottom-2 duration-300"
    >
      <div className="p-5">
        <div className="flex items-start gap-3">
          <div className="h-9 w-9 rounded-lg bg-amber-500/10 ring-1 ring-amber-500/20 flex items-center justify-center shrink-0">
            <Cookie className="h-4 w-4 text-amber-600 dark:text-amber-400" />
          </div>
          <div className="flex-1 min-w-0">
            <h3 className="font-semibold text-sm">Cookies & terms</h3>
            <p className="text-xs text-muted-foreground mt-1 leading-relaxed">
              We use essential cookies to keep you signed in and analytics
              cookies to improve the product. By continuing you accept our{" "}
              <Link
                href="/legal/terms"
                className="underline underline-offset-2 hover:text-foreground"
              >
                Terms of Service
              </Link>{" "}
              and{" "}
              <Link
                href="/legal/privacy"
                className="underline underline-offset-2 hover:text-foreground"
              >
                Privacy Policy
              </Link>
              .
            </p>
          </div>
          <button
            onClick={dismiss}
            aria-label="Dismiss"
            className="text-muted-foreground hover:text-foreground -mt-1 -mr-1 p-1"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="mt-4 flex items-center justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={dismiss}>
            Decide later
          </Button>
          <Button size="sm" onClick={accept}>
            Accept
          </Button>
        </div>
      </div>
    </div>
  );
}
