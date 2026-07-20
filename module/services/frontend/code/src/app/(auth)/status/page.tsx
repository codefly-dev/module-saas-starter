"use client";

/**
 * /status — public system health page. Lives under the (auth) route
 * group so it bypasses the dashboard layout's auth gate; intentional
 * — status pages MUST be reachable without a session, otherwise an
 * outage on auth itself hides the outage from customers.
 *
 * Polls /v1/status every 30s. Each component shows a colored dot
 * (green ok, amber degraded, red down) + latency. Empty state hides
 * the body if probes haven't been registered yet.
 */

import {
	Activity,
	AlertTriangle,
	CheckCircle2,
	RefreshCw,
	XCircle,
} from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { BrandMark } from "@/components/brand-mark";
import { useAppearance } from "@/lib/appearance-provider";

type ComponentStatus = "ok" | "degraded" | "down";

interface StatusComponent {
	name: string;
	status: ComponentStatus;
	latency_ms: number;
	error?: string;
}

interface StatusResponse {
	status: ComponentStatus;
	checked_at: string;
	components: StatusComponent[];
	uptime_seconds: number;
}

export default function StatusPage() {
	const { branding } = useAppearance();
	const [data, setData] = useState<StatusResponse | null>(null);
	const [error, setError] = useState<string | null>(null);
	const [loading, setLoading] = useState(true);

	const fetchStatus = useCallback(async () => {
		try {
			const res = await fetch("/v1/status", { cache: "no-store" });
			// 503 is a valid response shape (degraded/down) — read JSON regardless.
			const body = await res.json();
			setData(body);
			setError(null);
		} catch (e) {
			setError(e instanceof Error ? e.message : "fetch failed");
			setData(null);
		} finally {
			setLoading(false);
		}
	}, []);

	useEffect(() => {
		void fetchStatus();
		const id = window.setInterval(fetchStatus, 30_000);
		return () => window.clearInterval(id);
	}, [fetchStatus]);

	const overall = data?.status ?? (error ? "down" : "ok");

	return (
		<div className="min-h-screen bg-gradient-to-br from-background to-muted/60">
			<div className="max-w-3xl mx-auto px-4 py-16">
				{/* HEADER */}
				<div className="flex items-center gap-3 mb-2">
					<BrandMark
						className="flex h-10 w-10 items-center justify-center overflow-hidden rounded-xl bg-primary text-lg font-bold text-primary-foreground shadow-lg shadow-primary/25"
						imageClassName="p-1"
					/>
					<span className="text-xl font-semibold tracking-tight">
						{branding.name}
					</span>
				</div>
				<h1 className="text-3xl font-bold tracking-tight mt-8">
					System status
				</h1>
				<p className="text-muted-foreground mt-1.5">
					Live health probes update every 30 seconds. All times in your local
					zone.
				</p>

				{/* OVERALL */}
				<div className="mt-8 rounded-2xl border bg-card p-6 flex items-start gap-4">
					<StatusIcon status={overall} large />
					<div className="flex-1">
						<h2 className="text-xl font-semibold">{overallHeading(overall)}</h2>
						{data && (
							<p className="text-sm text-muted-foreground mt-1">
								Last checked {new Date(data.checked_at).toLocaleTimeString()} ·
								api uptime {formatUptime(data.uptime_seconds)}
							</p>
						)}
						{error && (
							<p className="text-sm text-red-600 dark:text-red-400 mt-1">
								Could not reach status endpoint: {error}
							</p>
						)}
					</div>
					<button
						type="button"
						onClick={() => void fetchStatus()}
						className="text-xs text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5"
						aria-label="Refresh"
					>
						<RefreshCw
							className={`h-3.5 w-3.5 ${loading ? "animate-spin" : ""}`}
						/>
						Refresh
					</button>
				</div>

				{/* COMPONENTS */}
				{data && data.components.length > 0 && (
					<div className="mt-6 rounded-2xl border bg-card divide-y">
						{data.components.map((c) => (
							<div
								key={c.name}
								className="flex items-center justify-between px-6 py-4"
							>
								<div className="flex items-center gap-3">
									<StatusIcon status={c.status} />
									<div>
										<div className="font-medium capitalize">{c.name}</div>
										{c.error && (
											<div className="text-xs text-red-600 dark:text-red-400 mt-0.5">
												{c.error}
											</div>
										)}
									</div>
								</div>
								<div className="text-xs text-muted-foreground font-mono">
									{c.latency_ms} ms
								</div>
							</div>
						))}
					</div>
				)}

				{data && data.components.length === 0 && (
					<div className="mt-6 rounded-2xl border bg-card px-6 py-8 text-center text-sm text-muted-foreground">
						No probes registered.
					</div>
				)}

				<p className="mt-8 text-xs text-muted-foreground text-center">
					<Activity className="inline h-3 w-3 mr-1" />
					This page is publicly reachable and intentionally bypasses auth.
				</p>
			</div>
		</div>
	);
}

function StatusIcon({
	status,
	large,
}: {
	status: ComponentStatus;
	large?: boolean;
}) {
	const size = large ? "h-8 w-8" : "h-5 w-5";
	switch (status) {
		case "ok":
			return <CheckCircle2 className={`${size} text-emerald-500`} />;
		case "degraded":
			return <AlertTriangle className={`${size} text-amber-500`} />;
		case "down":
			return <XCircle className={`${size} text-red-500`} />;
	}
}

function overallHeading(s: ComponentStatus): string {
	switch (s) {
		case "ok":
			return "All systems operational";
		case "degraded":
			return "Partial outage";
		case "down":
			return "Major outage";
	}
}

function formatUptime(seconds: number): string {
	if (seconds < 60) return `${seconds}s`;
	if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
	if (seconds < 86400)
		return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
	const days = Math.floor(seconds / 86400);
	const hours = Math.floor((seconds % 86400) / 3600);
	return `${days}d ${hours}h`;
}
