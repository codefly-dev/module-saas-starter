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

import { AlertTriangle } from "lucide-react";
import { useSyncExternalStore } from "react";
import {
	getRateLimit,
	subscribeRateLimit,
} from "@/lib/connect/rate-limit-tracker";

const WARN_RATIO = 0.1; // banner kicks in when remaining/limit < 10%
const CLOCK_INTERVAL_MS = 1000;

function subscribeToRateLimit(onStoreChange: () => void) {
	return subscribeRateLimit(onStoreChange);
}

function getServerRateLimit() {
	return null;
}

function subscribeToClock(onStoreChange: () => void) {
	const timer = window.setInterval(onStoreChange, CLOCK_INTERVAL_MS);
	return () => window.clearInterval(timer);
}

function getClockSeconds() {
	return Math.floor(Date.now() / 1000);
}

function getServerClockSeconds() {
	return 0;
}

export function RateLimitBanner() {
	const snap = useSyncExternalStore(
		subscribeToRateLimit,
		getRateLimit,
		getServerRateLimit,
	);
	const now = useSyncExternalStore(
		subscribeToClock,
		getClockSeconds,
		getServerClockSeconds,
	);
	const seconds = snap && now > 0 ? Math.max(0, snap.resetAt - now) : null;

	if (!snap) return null;
	if (seconds === 0) return null;
	if (snap.remaining / snap.limit >= WARN_RATIO) return null;

	return (
		<div className="fixed bottom-4 left-1/2 -translate-x-1/2 z-50 flex items-center gap-2 rounded-full border border-amber-500/40 bg-amber-500/10 backdrop-blur px-4 py-2 text-xs font-medium text-amber-900 dark:text-amber-200 shadow-lg animate-in fade-in slide-in-from-bottom-2 duration-200">
			<AlertTriangle className="h-3.5 w-3.5" />
			<span>
				Rate limit running low — {snap.remaining} of {snap.limit} requests left.
				Resets in {seconds ?? "a few"}s.
			</span>
		</div>
	);
}
