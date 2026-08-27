// A deterministic, framework-agnostic stand-in for the external conversational
// driver that will eventually author dashboards. It maps a plain-English command
// to a new DashboardDef — the same effect a chat agent's `setDashboard` turn
// would have — with zero model calls, so the whole "describe → mutate the live
// dashboard" loop is testable here without any agent service.
//
// Interface-first: a real agent replaces `applyCommand` behind the same
// signature (current spec + text → next spec + note). The canvas, the draft
// store, and <Dashboard> never learn which driver produced the spec.

import type { ChartKind, DashboardDef, GroupBy, MetricDef } from "../model/schema";
import { validateDashboardDef } from "../model/validate";

export interface DriverResult {
	dashboard: DashboardDef;
	// A short line describing what the command did, for the dev UI to echo.
	note: string;
}

// A tiny lexicon so the stub grounds metrics in real audit events instead of
// inventing event names — the same job listEventTypes() does for a real agent.
const EVENT_LEXICON: ReadonlyArray<{ match: RegExp; type: string; label: string }> = [
	{ match: /\b(login|logins|log ?in|signed? ?in)\b/i, type: "auth.login", label: "Logins" },
	{ match: /\b(mfa|totp|2fa|two.?factor)\b/i, type: "mfa.totp_verified", label: "TOTP verifications" },
	{ match: /\b(logout|log ?out|signed? ?out)\b/i, type: "auth.logout", label: "Logouts" },
];

function detectChart(text: string): ChartKind {
	if (/\b(total|count|number|kpi|stat|how many)\b/i.test(text)) return "stat";
	if (/\b(bar|top|rank|ranked|breakdown|by )\b/i.test(text)) return "bar";
	return "line"; // "over time" / "trend" and the sensible default
}

function detectGroupBy(text: string, chart: ChartKind): GroupBy {
	if (/\bby category\b/i.test(text)) return "category";
	if (/\bby (event|type|action)\b/i.test(text)) return "event_type";
	if (/\bby (actor|user|person)\b/i.test(text)) return "actor";
	if (/\b(over time|trend|daily|weekly|monthly|per (day|week|month))\b/i.test(text)) {
		return "time";
	}
	// A stat/line with no explicit dimension reads most naturally as a time series;
	// a bar with no dimension ranks event types.
	return chart === "bar" ? "event_type" : "time";
}

function detectBucket(text: string): "day" | "week" | "month" | undefined {
	if (/\b(week|weekly)\b/i.test(text)) return "week";
	if (/\b(month|monthly)\b/i.test(text)) return "month";
	if (/\b(day|daily)\b/i.test(text)) return "day";
	return undefined;
}

// Turn one command into the metric it describes. Exported so a test (or the dev
// UI's "preview" affordance) can inspect the parse without mutating a dashboard.
export function metricFromCommand(text: string): MetricDef {
	const chart = detectChart(text);
	const groupBy = detectGroupBy(text, chart);
	const event = EVENT_LEXICON.find((e) => e.match.test(text));
	const category = /\bsecurity\b/i.test(text) ? "security" : undefined;

	const metric: MetricDef = {
		title: describeMetric({ event: event?.label, category, chart, groupBy }),
		groupBy,
		chart,
		...(event ? { event: { type: event.type } } : {}),
		...(category ? { category } : {}),
		...(groupBy === "time" ? { bucket: detectBucket(text) ?? "day" } : {}),
		...(chart === "bar" ? { limit: 6 } : {}),
	};
	return metric;
}

function describeMetric(parts: {
	event?: string;
	category?: string;
	chart: ChartKind;
	groupBy: GroupBy;
}): string {
	const subject = parts.event ?? (parts.category ? `${parts.category} events` : "Events");
	if (parts.chart === "stat") return `Total ${subject.toLowerCase()}`;
	if (parts.groupBy === "time") return `${subject} over time`;
	return `${subject} by ${parts.groupBy.replace("_", " ")}`;
}

const EMPTY: DashboardDef = { title: "Untitled dashboard", metrics: [] };

// Apply one natural-language command to the current dashboard and return the
// next one. Recognizes a few structural verbs (clear / remove) and otherwise
// treats the command as "add a metric that shows …". Always returns a validated
// spec; if a parse ever produced an invalid metric it is reported, not applied.
export function applyCommand(
	current: DashboardDef | undefined,
	text: string,
): DriverResult {
	const base = current ?? EMPTY;
	const trimmed = text.trim();

	if (trimmed === "") {
		return { dashboard: base, note: "Nothing to do." };
	}
	if (/^\s*(clear|reset|start over|empty)\b/i.test(trimmed)) {
		return { dashboard: { ...base, metrics: [] }, note: "Cleared all widgets." };
	}
	if (/^\s*(remove|delete|drop)\b.*\blast\b/i.test(trimmed)) {
		if (base.metrics.length === 0) {
			return { dashboard: base, note: "No widgets to remove." };
		}
		return {
			dashboard: { ...base, metrics: base.metrics.slice(0, -1) },
			note: `Removed "${base.metrics[base.metrics.length - 1].title}".`,
		};
	}

	const metric = metricFromCommand(trimmed);
	const next: DashboardDef = { ...base, metrics: [...base.metrics, metric] };
	const result = validateDashboardDef(next);
	if (!result.ok) {
		// Mirrors how a real agent turn surfaces a rejected spec: keep the current
		// dashboard, hand back the reason for a correction pass.
		return { dashboard: base, note: `Could not add widget (${result.path}: ${result.message}).` };
	}
	return { dashboard: next, note: `Added "${metric.title}".` };
}
