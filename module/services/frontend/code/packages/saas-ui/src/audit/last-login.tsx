"use client";

import { useEffect, useState } from "react";
import { cn } from "../datasources/util.js";

/** The units a relative time is said in, largest first, in seconds. */
const UNITS: readonly [Intl.RelativeTimeFormatUnit, number][] = [
	["year", 365 * 24 * 3600],
	["month", 30 * 24 * 3600],
	["week", 7 * 24 * 3600],
	["day", 24 * 3600],
	["hour", 3600],
	["minute", 60],
	["second", 1],
];

/**
 * How long ago `at` was, from `now`, in words: "12 seconds ago", "3 hours ago",
 * "yesterday", counting whole units elapsed. Pure, so a consumer can say it in its own shell. A moment in the
 * future (a clock ahead of this one) reads as "now" rather than "in 2 seconds".
 */
export function relativeTime(at: Date, now: Date, locale?: string): string {
	const seconds = Math.round((at.getTime() - now.getTime()) / 1000);
	const format = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
	if (seconds >= 0) return format.format(0, "second");
	for (const [unit, size] of UNITS) {
		// Whole units elapsed: 90 seconds is "1 minute ago" until two have passed.
		if (-seconds >= size || unit === "second") return format.format(Math.trunc(seconds / size), unit);
	}
	return format.format(seconds, "second");
}

/** How often the words must be re-said to stay true. */
function tick(at: Date, now: Date): number {
	const elapsed = now.getTime() - at.getTime();
	return elapsed < 60_000 ? 1_000 : elapsed < 3_600_000 ? 30_000 : 300_000;
}

export interface LastLoginProps {
	/** When the person last signed in, as an ISO string or a Date; null when
	 *  no sign-in is recorded. */
	at: string | Date | null;
	/** Who signed in, when it should be said (an email, a name). */
	subject?: string;
	/** The label above the time. Default "Last login". */
	label?: string;
	/** The clock, for tests and stories. Default the system clock. */
	now?: () => Date;
	className?: string;
}

/**
 * When someone last signed in, as a person reads it: "Last login · 12 seconds
 * ago", kept true as time passes, with the exact time on hover and for
 * assistive technology (`<time dateTime title>`). A solution hands it the value
 * it read — from the audit log, say — and owns nothing of how it is said.
 *
 * It is the kit's rather than each solution's because every page that shows a
 * sign-in time otherwise writes its own "x ago" with its own thresholds; the
 * host already has two that disagree.
 */
export function LastLogin({ at, subject, label = "Last login", now = () => new Date(), className }: LastLoginProps) {
	const when = at === null ? null : at instanceof Date ? at : new Date(at);
	const atMs = when === null ? Number.NaN : when.getTime();
	const valid = !Number.isNaN(atMs);
	const [current, setCurrent] = useState(now);
	useEffect(() => {
		if (Number.isNaN(atMs)) return;
		const timer = setInterval(() => setCurrent(now()), tick(new Date(atMs), current));
		return () => clearInterval(timer);
	}, [atMs, current, now]);

	return (
		<div data-slot="last-login" className={cn("space-y-1", className)}>
			<div className="type-metric-label text-muted-foreground">{label}</div>
			{valid ? (
				<>
					<time dateTime={when!.toISOString()} title={when!.toLocaleString()} className="type-metric-value-lg tabular-nums">
						{relativeTime(when!, current)}
					</time>
					<p className="type-caption-plain text-muted-foreground">
						{when!.toLocaleString()}
						{subject ? <> · {subject}</> : null}
					</p>
				</>
			) : (
				<>
					<div className="type-metric-value-lg text-muted-foreground">Never</div>
					<p className="type-caption-plain text-muted-foreground">No sign-in is recorded yet.</p>
				</>
			)}
		</div>
	);
}
