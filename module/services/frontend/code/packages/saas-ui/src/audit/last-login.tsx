"use client";

import { useEffect, useRef, useState } from "react";
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
 * "yesterday", counting whole units elapsed: 59.999 seconds is still "59
 * seconds ago". Pure, so a consumer can say it in its own shell. A moment in
 * the future (a clock ahead of this one) reads as "now" rather than "in 2
 * seconds".
 */
export function relativeTime(at: Date, now: Date, locale?: string): string {
	const format = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
	const elapsed = Math.floor((now.getTime() - at.getTime()) / 1000);
	if (elapsed <= 0) return format.format(0, "second");
	for (const [unit, size] of UNITS) {
		if (elapsed >= size) return format.format(-Math.floor(elapsed / size), unit);
	}
	return format.format(0, "second");
}

/** How long until the words must be re-said to stay true. */
function cadence(at: number, now: number): number {
	const elapsed = now - at;
	return elapsed < 60_000 ? 1_000 : elapsed < 3_600_000 ? 30_000 : 300_000;
}

/** The system clock, one function for every render so it is never a new
 *  dependency. */
const systemNow = () => new Date();

/** What the server renders and the browser's first render repeats: the time in
 *  UTC, which depends on neither environment's clock, timezone nor locale. */
function stableTime(at: Date): string {
	return `${at.toISOString().slice(0, 16).replace("T", " ")} UTC`;
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
 * The first render, on the server and again in the browser while it hydrates,
 * says the time in UTC, which no clock or timezone can change between the two;
 * once mounted it reads the browser's clock and says it relatively, in the
 * viewer's own timezone. Its re-saying is scheduled from the time itself, so a
 * parent that re-renders often cannot hold it still.
 *
 * It is the kit's rather than each solution's because every page that shows a
 * sign-in time otherwise writes its own "x ago" with its own thresholds; the
 * host already has two that disagree.
 */
export function LastLogin({ at, subject, label = "Last login", now = systemNow, className }: LastLoginProps) {
	const when = at === null ? null : at instanceof Date ? at : new Date(at);
	const atMs = when === null ? Number.NaN : when.getTime();
	const valid = !Number.isNaN(atMs);
	// The clock is read through a ref, so a caller handing a new function on
	// every render does not restart the schedule below.
	const clock = useRef(now);
	clock.current = now;
	// null until mounted: the first render is the hydration render.
	const [current, setCurrent] = useState<Date | null>(null);
	useEffect(() => {
		if (Number.isNaN(atMs)) return;
		let timer: ReturnType<typeof setTimeout>;
		const say = () => {
			const read = clock.current();
			setCurrent(read);
			timer = setTimeout(say, cadence(atMs, read.getTime()));
		};
		say();
		return () => clearTimeout(timer);
	}, [atMs]);

	return (
		<div data-slot="last-login" className={cn("space-y-1", className)}>
			<div className="type-metric-label text-muted-foreground">{label}</div>
			{valid ? (
				<>
					<time
						dateTime={when!.toISOString()}
						title={current ? when!.toLocaleString() : stableTime(when!)}
						className="type-metric-value-lg tabular-nums"
					>
						{current ? relativeTime(when!, current) : stableTime(when!)}
					</time>
					<p className="type-caption-plain text-muted-foreground">
						{current ? when!.toLocaleString() : stableTime(when!)}
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
