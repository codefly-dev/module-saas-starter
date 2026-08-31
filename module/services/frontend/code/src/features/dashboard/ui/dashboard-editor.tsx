"use client";

import { ArrowDown, ArrowUp, Trash2 } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import type { AuditEventTypeInfo } from "@/features/audit";
import { useAuth } from "@/lib/auth";
import {
	Button,
	Card,
	CardContent,
	CardHeader,
	CardTitle,
	Input,
	Label,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Separator,
} from "@/shared/ui";
import { metricIdentity } from "../model/identity";
import type {
	Bucket,
	ChartKind,
	DashboardDef,
	Dimension,
	MetricDef,
} from "../model/schema";
import type { FieldError } from "../model/validate";
import { useDashboardAuthoring } from "../service/use-dashboard-authoring";
import { Dashboard } from "./dashboard";

// The subset of group dimensions the structural editor offers. The spec permits
// payload fields too, but those need a key the audit registry doesn't enumerate,
// so the form stays on the four fixed columns a picker can ground.
const GROUP_BY_OPTIONS: readonly { value: Dimension; label: string }[] = [
	{ value: "time", label: "Over time" },
	{ value: "event_type", label: "By event type" },
	{ value: "category", label: "By category" },
	{ value: "actor", label: "By actor" },
];

const CHART_OPTIONS: readonly { value: ChartKind; label: string }[] = [
	{ value: "line", label: "Line" },
	{ value: "bar", label: "Bar" },
	{ value: "stat", label: "Stat" },
];

const BUCKET_OPTIONS: readonly { value: Bucket; label: string }[] = [
	{ value: "day", label: "Daily" },
	{ value: "week", label: "Weekly" },
	{ value: "month", label: "Monthly" },
];

// The <Select> can't hold an empty-string value, so the "all events" choice
// rides this sentinel and maps back to an unscoped metric.
const ALL_EVENTS = "__all__";

interface WidgetForm {
	eventType: string;
	groupBy: Dimension;
	chart: ChartKind;
	bucket: Bucket;
	title: string;
}

const DEFAULT_FORM: WidgetForm = {
	eventType: ALL_EVENTS,
	groupBy: "time",
	chart: "line",
	bucket: "day",
	title: "",
};

function autoTitle(form: WidgetForm): string {
	const subject = form.eventType === ALL_EVENTS ? "Events" : form.eventType;
	if (form.chart === "stat") return `Total ${subject}`;
	const grouping = GROUP_BY_OPTIONS.find((o) => o.value === form.groupBy);
	return `${subject} ${grouping?.label.toLowerCase() ?? form.groupBy}`;
}

// Lower the form to a metric. A time grouping carries the chosen bucket; every
// other grouping carries none — exactly the coherence the spec validator (and
// setDashboard) enforces, so a form-built metric is always valid by construction
// and the error channel is reserved for genuinely rejected specs (e.g. an event
// that stops resolving).
function metricFromForm(form: WidgetForm): MetricDef {
	return {
		title: form.title.trim() || autoTitle(form),
		groupBy: form.groupBy,
		chart: form.chart,
		...(form.eventType === ALL_EVENTS
			? {}
			: { event: { type: form.eventType } }),
		...(form.groupBy === "time" ? { bucket: form.bucket } : {}),
	};
}

// DashboardEditor is the first on-screen entry point for the runtime-editing
// loop: it mounts the authoring contract against the live audit client and the
// persisted draft, renders the draft through the same <Dashboard> canvas a
// static page uses, and lets a signed-in user add, remove, and reorder widgets
// from a structural form grounded in the real audit event registry. Every commit
// runs through authoring.setDashboard, so a spec is validated against the
// vocabulary before it re-renders and any rejection surfaces as data rather than
// a broken render. Persistence is still the draft hook's concern (localStorage
// today); this surface is agnostic to where the draft lives.
export function DashboardEditor({
	storageKey,
	initial,
}: {
	storageKey: string;
	initial: DashboardDef;
}) {
	const { user, organizationId } = useAuth();
	// Scope the persisted draft to the viewer and their org: the draft lives in
	// per-browser localStorage, so an unscoped key would restore one user's (or
	// one org's) widgets under another's session on a shared browser.
	const scopedKey = `${storageKey}:${organizationId ?? "none"}:${user?.id ?? "anon"}`;
	const { authoring, draft } = useDashboardAuthoring(scopedKey, initial);
	const spec = draft.spec;

	const [events, setEvents] = useState<AuditEventTypeInfo[]>([]);
	const [form, setForm] = useState<WidgetForm>(DEFAULT_FORM);
	const [errors, setErrors] = useState<FieldError[]>([]);
	const [pending, setPending] = useState<string | null>(null);
	const [preview, setPreview] = useState<{
		total: number;
		points: number;
	} | null>(null);

	useEffect(() => {
		let active = true;
		authoring
			.listEventTypes()
			.then((vocab) => {
				if (active) setEvents(vocab.events);
			})
			.catch(() => {
				// A vocabulary read failure leaves the picker on "all events"; the
				// commit path still validates, so it degrades rather than blocks.
			});
		return () => {
			active = false;
		};
	}, [authoring]);

	// Commit a whole spec through the authoring contract so persistence and the
	// vocabulary check share one path. Returns whether it committed, so a caller
	// can clear its form only on success.
	const commit = useCallback(
		async (next: DashboardDef): Promise<boolean> => {
			const result = await authoring.setDashboard(next);
			if (!result.ok) {
				setErrors(result.errors);
				return false;
			}
			setErrors([]);
			return true;
		},
		[authoring],
	);

	const onPreview = useCallback(async () => {
		setPending(null);
		const result = await authoring.previewMetric(metricFromForm(form));
		if (result.ok) {
			setErrors([]);
			setPreview({
				total: result.preview.total,
				points: result.preview.points.length,
			});
			return;
		}
		setPreview(null);
		if (result.kind === "pending") {
			setPending(result.message);
			return;
		}
		setErrors(result.errors);
	}, [authoring, form]);

	const onAdd = useCallback(async () => {
		setPending(null);
		setPreview(null);
		const metric = metricFromForm(form);
		// metricIdentity is the React key <Dashboard> and the widget list both use,
		// so two metrics that share it collide on that key. Reject the duplicate
		// here — the same guard the stub driver's applyCommand upholds — rather than
		// commit a spec that renders two cards under one key.
		const identity = metricIdentity(metric);
		if (spec.metrics.some((m) => metricIdentity(m) === identity)) {
			setErrors([
				{
					code: "duplicate_metric",
					message: `This dashboard already shows "${metric.title}".`,
				},
			]);
			return;
		}
		if (await commit({ ...spec, metrics: [...spec.metrics, metric] })) {
			setForm(DEFAULT_FORM);
		}
	}, [commit, form, spec]);

	const onRemove = useCallback(
		async (index: number) => {
			await commit({
				...spec,
				metrics: spec.metrics.filter((_, i) => i !== index),
			});
		},
		[commit, spec],
	);

	const onMove = useCallback(
		async (index: number, delta: -1 | 1) => {
			const target = index + delta;
			if (target < 0 || target >= spec.metrics.length) return;
			const metrics = [...spec.metrics];
			[metrics[index], metrics[target]] = [metrics[target], metrics[index]];
			await commit({ ...spec, metrics });
		},
		[commit, spec],
	);

	const update = <K extends keyof WidgetForm>(key: K, value: WidgetForm[K]) =>
		setForm((prev) => ({ ...prev, [key]: value }));

	return (
		<div className="grid gap-6 lg:grid-cols-[minmax(0,20rem)_minmax(0,1fr)]">
			<Card>
				<CardHeader>
					<CardTitle>Add a widget</CardTitle>
				</CardHeader>
				<CardContent className="space-y-4">
					<div className="space-y-2">
						<Label htmlFor="widget-event">Event</Label>
						<Select
							value={form.eventType}
							onValueChange={(value) =>
								update("eventType", value ?? ALL_EVENTS)
							}
						>
							<SelectTrigger id="widget-event" aria-label="Event">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value={ALL_EVENTS}>All events</SelectItem>
								{events.map((entry) => (
									<SelectItem key={entry.name} value={entry.name}>
										{entry.name}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>

					<div className="space-y-2">
						<Label htmlFor="widget-group">Group by</Label>
						<Select
							value={form.groupBy}
							onValueChange={(value) => {
								if (value) update("groupBy", value as Dimension);
							}}
						>
							<SelectTrigger id="widget-group" aria-label="Group by">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{GROUP_BY_OPTIONS.map((option) => (
									<SelectItem key={option.value} value={option.value}>
										{option.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>

					{form.groupBy === "time" && (
						<div className="space-y-2">
							<Label htmlFor="widget-bucket">Interval</Label>
							<Select
								value={form.bucket}
								onValueChange={(value) => {
									if (value) update("bucket", value as Bucket);
								}}
							>
								<SelectTrigger id="widget-bucket" aria-label="Interval">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{BUCKET_OPTIONS.map((option) => (
										<SelectItem key={option.value} value={option.value}>
											{option.label}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
					)}

					<div className="space-y-2">
						<Label htmlFor="widget-chart">Chart</Label>
						<Select
							value={form.chart}
							onValueChange={(value) => {
								if (value) update("chart", value as ChartKind);
							}}
						>
							<SelectTrigger id="widget-chart" aria-label="Chart">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{CHART_OPTIONS.map((option) => (
									<SelectItem key={option.value} value={option.value}>
										{option.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>

					<div className="space-y-2">
						<Label htmlFor="widget-title">Title</Label>
						<Input
							id="widget-title"
							value={form.title}
							placeholder={autoTitle(form)}
							onChange={(e) => update("title", e.target.value)}
						/>
					</div>

					<div className="flex gap-2">
						<Button type="button" variant="outline" onClick={onPreview}>
							Preview
						</Button>
						<Button type="button" onClick={onAdd}>
							Add widget
						</Button>
					</div>

					{preview && (
						<p className="text-sm text-muted-foreground" role="status">
							Preview: {preview.total} across {preview.points} point
							{preview.points === 1 ? "" : "s"}.
						</p>
					)}
					{pending && (
						<p className="text-sm text-muted-foreground" role="status">
							{pending}
						</p>
					)}
					{(errors.length > 0 || draft.error) && (
						<div className="space-y-1 text-sm text-destructive" role="alert">
							{draft.error && <p>{draft.error.message}</p>}
							{errors.map((error) => (
								<p key={`${error.path ?? ""}:${error.message}`}>
									{error.message}
								</p>
							))}
						</div>
					)}
				</CardContent>
			</Card>

			<div className="space-y-4">
				<Card>
					<CardHeader>
						<CardTitle>Widgets</CardTitle>
					</CardHeader>
					<CardContent className="space-y-2">
						{spec.metrics.length === 0 ? (
							<p className="text-sm text-muted-foreground">
								No widgets yet. Add one from the panel to start your dashboard.
							</p>
						) : (
							spec.metrics.map((metric, index) => (
								<div
									key={metricIdentity(metric)}
									className="flex items-center justify-between gap-2"
								>
									<span className="truncate text-sm">{metric.title}</span>
									<div className="flex shrink-0 gap-1">
										<Button
											type="button"
											variant="ghost"
											size="icon"
											aria-label={`Move "${metric.title}" up`}
											disabled={index === 0}
											onClick={() => onMove(index, -1)}
										>
											<ArrowUp className="size-4" />
										</Button>
										<Button
											type="button"
											variant="ghost"
											size="icon"
											aria-label={`Move "${metric.title}" down`}
											disabled={index === spec.metrics.length - 1}
											onClick={() => onMove(index, 1)}
										>
											<ArrowDown className="size-4" />
										</Button>
										<Button
											type="button"
											variant="ghost"
											size="icon"
											aria-label={`Remove "${metric.title}"`}
											onClick={() => onRemove(index)}
										>
											<Trash2 className="size-4" />
										</Button>
									</div>
								</div>
							))
						)}
					</CardContent>
				</Card>

				<Separator />

				<Dashboard data={spec} />
			</div>
		</div>
	);
}
