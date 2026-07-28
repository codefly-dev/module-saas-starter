"use client";

/**
 * ConsentBanner — cookie + TOS acceptance banner. Two-tier flow:
 *
 *  - Authenticated users: source of truth is the api
 *    (ConsentService.GetStatus / Accept). Records userID + version +
 *    timestamp + IP server-side and emits an audit event.
 *
 *  - Anonymous visitors: localStorage fallback (per-browser). Good
 *    enough for marketing pages where there's no user row to attach
 *    to. On login we re-check via the server, so a marketing-page
 *    "Accept" doesn't override the authoritative server record.
 *
 * The api owns the current TOS version (CurrentTermsVersion in
 * pkg/business/consent.go). For the anonymous path we keep a local
 * constant — bumped together with the api — so we can decide whether
 * to show the banner before any RPC fires.
 */

import { createClient } from "@connectrpc/connect";
import { Cookie, X } from "lucide-react";
import Link from "next/link";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { ConsentService } from "@/gen/saas/accounts/v1/consent_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";

// Anonymous-fallback version. Should match
// pkg/business/consent.go:CurrentTermsVersion. Drift is harmless —
// authenticated users never read this constant.
const ANON_CONSENT_VERSION = "2026-04-25";
const STORAGE_KEY = "saas-starter:consent-version";

const consentClient = createClient(ConsentService, apiTransport);

type BannerState = "loading" | "stale" | "current";
type ConsentMode = "authenticated" | "anonymous";

interface ConsentResult {
	mode: ConsentMode;
	state: Exclude<BannerState, "loading">;
}

export function ConsentBanner() {
	const { isAuthenticated, isLoading: authLoading } = useAuth();
	const mode: ConsentMode = isAuthenticated ? "authenticated" : "anonymous";
	const [result, setResult] = useState<ConsentResult | null>(null);

	useEffect(() => {
		// While auth is resolving (refresh-token round-trip on cold load)
		// we render nothing — picking a tier early would either flash the
		// banner for users who already accepted server-side, or skip it
		// for someone whose server record disagrees with localStorage.
		if (authLoading) return;

		let cancelled = false;

		if (mode === "authenticated") {
			consentClient
				.getStatus({})
				.then((status) => {
					if (cancelled) return;
					const accepted =
						status.acceptedVersion !== "" &&
						status.acceptedVersion === status.currentVersion;
					setResult({
						mode: "authenticated",
						state: accepted ? "current" : "stale",
					});
				})
				.catch(() => {
					// Server unreachable → fall back to "current" so a transient
					// outage doesn't pop the banner over real content. Next page
					// load retries.
					if (!cancelled) {
						setResult({ mode: "authenticated", state: "current" });
					}
				});
		} else {
			// Read browser storage asynchronously so the effect only synchronizes
			// with the external store and never performs a synchronous state cascade.
			Promise.resolve().then(() => {
				if (cancelled) return;
				try {
					const accepted = window.localStorage.getItem(STORAGE_KEY);
					setResult({
						mode: "anonymous",
						state: accepted === ANON_CONSENT_VERSION ? "current" : "stale",
					});
				} catch {
					// SSR / private mode → no banner.
					setResult({ mode: "anonymous", state: "current" });
				}
			});
		}

		return () => {
			cancelled = true;
		};
	}, [mode, authLoading]);

	const state: BannerState =
		authLoading || result?.mode !== mode ? "loading" : result.state;

	async function accept() {
		if (isAuthenticated) {
			try {
				const status = await consentClient.accept({
					version: ANON_CONSENT_VERSION,
				});
				// Server echoes the version it actually persisted; trust that.
				if (status.acceptedVersion === status.currentVersion) {
					setResult({ mode, state: "current" });
					return;
				}
			} catch {
				// Don't strand the user in a banner loop on a transient error
				// — close it locally; they'll re-see it on next page load if
				// the server still has no record.
			}
			setResult({ mode, state: "current" });
			return;
		}
		try {
			window.localStorage.setItem(STORAGE_KEY, ANON_CONSENT_VERSION);
		} catch {
			// ignore — SSR / private mode
		}
		setResult({ mode, state: "current" });
	}

	function dismiss() {
		// Dismissing is NOT acceptance — banner returns next visit. We
		// close it locally for this session only; nothing is persisted.
		setResult({ mode, state: "current" });
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
