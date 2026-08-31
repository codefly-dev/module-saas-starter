"use client";

import { GripVertical, Plus, Trash2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import {
	Button,
	Card,
	CardContent,
	CardHeader,
	Input,
	Label,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Separator,
} from "@/shared/ui";
import {
	type Bucket,
	type ChartKind,
	DASHBOARD_SPEC_VERSION,
	type DashboardDef,
	type Dimension,
	dashboard,
	type MetricDef,
} from "../model/schema";
import type { FieldError } from "../model/validate";
import type { EventTypeVocabulary, PreviewResult } from "../service/authoring";
import { useDashboardAuthoring } from "../service/use-dashboard-authoring";
import { Dashboard } from "./dashboard";

// The editor works on a small window of the schema — the structural knobs a
// user picks — so each control maps to one dropdown. The full DSL (payload
// dimensions, ratios, percentile, per-metric time windows) stays reachable
// through code-authored specs; this surface is the first-run subset.

// A field-taking aggregation. percentile is left to code-authored specs — it
// needs a second (quantile) input this first surface skips.
type FieldOp = "count_distinct" | "sum" | "avg" | "min" | "max";

const GROUP_BY: Dimension[] = ["event_type", "category", "actor", "time"];
const CHART: ChartKind[] = ["line", "bar", "stat"];
const BUCKET: Bucket[] = ["day", "week", "month"];
// count needs no field; the rest read one, so the field input appears for them.
const OP: (FieldOp | "count")[] = [
	"count",
	"count_distinct",
	"sum",
	"avg",
	"min",
	"max",
];

const GROUP_BY_LABEL: Record<Dimension, string> = {
	event_type: "Event type",
	category: "Category",
	actor: "Actor",
	time: "Time",
};

// A stable row identity so a widget keeps input focus across edits and can be
// reordered independently of its content (unlike metricIdentity, which changes
// as the metric is edited).
interface WidgetRow {
	id: string;
	metric: MetricDef;
}

interface EditorState {
	title: string;
	description: string;
	columns: 1 | 2 | 3 | 4;
	rows: WidgetRow[];
}

// Derive the canonical spec the editor commits from its row-keyed state. Empty
// title/description are omitted rather than sent as empty strings, which the
// validator rejects.
function deriveSpec(state: EditorState): DashboardDef {
	return {
		version: DASHBOARD_SPEC_VERSION,
		...(state.title.trim() ? { title: state.title } : {}),
		...(state.description.trim() ? { description: state.description } : {}),
		layout: { kind: "grid", columns: state.columns },
		metrics: state.rows.map((row) => row.metric),
	};
}

// "all" rather than "" because the Select forbids an empty item value.
function scopeValue(metric: MetricDef): string {
	if (metric.event) return `event:${metric.event.type}`;
	if (metric.category) return `category:${metric.category}`;
	return "all";
}

// event and category are mutually exclusive scopings, so setting one clears the
// other; the empty value clears both (aggregate everything). Cleared fields are
// set to undefined rather than deleted — the validator allows the key and JSON
// drops it, so the committed spec is identical either way.
function withScope(metric: MetricDef, value: string): MetricDef {
	if (value.startsWith("event:")) {
		return {
			...metric,
			event: { type: value.slice("event:".length) },
			category: undefined,
		};
	}
	if (value.startsWith("category:")) {
		return {
			...metric,
			event: undefined,
			category: value.slice("category:".length),
		};
	}
	return { ...metric, event: undefined, category: undefined };
}

// Picking time requires a bucket and forbids a limit; leaving time drops the
// bucket. Keeping the spec coherent on every select change means a structural
// edit commits and repaints the canvas immediately, rather than parking in an
// error state until a second field is set.
function withGroupBy(metric: MetricDef, groupBy: Dimension): MetricDef {
	if (groupBy === "time") {
		return {
			...metric,
			groupBy,
			bucket: metric.bucket ?? "day",
			limit: undefined,
		};
	}
	return { ...metric, groupBy, bucket: undefined };
}

function fieldOf(metric: MetricDef): string {
	const value = metric.value;
	return value && value.op !== "count" ? value.field : "";
}

// count carries no field; every field-taking op keeps whatever field was typed,
// so switching between them does not force a retype. `ratio` is cleared because
// value and ratio are mutually exclusive (this surface never authors a ratio).
function withOp(metric: MetricDef, op: FieldOp | "count"): MetricDef {
	if (op === "count") return { ...metric, value: undefined, ratio: undefined };
	return {
		...metric,
		value: { op, field: fieldOf(metric) },
		ratio: undefined,
	};
}

// Only the value's field changes; the op (and a percentile's quantile) is
// preserved so the discriminated shape stays intact.
function withField(metric: MetricDef, field: string): MetricDef {
	const value = metric.value;
	if (!value || value.op === "count") return metric;
	return { ...metric, value: { ...value, field }, ratio: undefined };
}

function opOf(metric: MetricDef): FieldOp | "count" {
	const op = metric.value?.op ?? "count";
	return op === "percentile" ? "count" : op;
}

function newWidget(index: number): MetricDef {
	// A distinct title per add keeps two fresh widgets from colliding on the
	// metricIdentity <Dashboard> keys on.
	return {
		title: index === 0 ? "New widget" : `New widget ${index + 1}`,
		groupBy: "event_type",
		chart: "bar",
	};
}

function seedState(spec: DashboardDef): EditorState {
	return {
		title: spec.title ?? "",
		description: spec.description ?? "",
		columns: spec.layout?.columns ?? 2,
		rows: spec.metrics.map((metric, index) => ({
			id: `w${index}`,
			metric,
		})),
	};
}

// The empty dashboard a first-time visitor starts from: no widgets, a title
// they can rename. Held to the same validation as any spec.
const INITIAL: DashboardDef = dashboard({
	title: "My dashboard",
	description: "Build a view over your organization's audit trail.",
	layout: { kind: "grid", columns: 2 },
	metrics: [],
});

const STORAGE_KEY = "dashboard:authoring";

function errorList(errors: FieldError[], prefix?: string) {
	return (
		<ul className="space-y-1 text-sm text-destructive">
			{errors.map((error) => (
				<li key={`${error.path ?? ""}:${error.code}`}>
					{prefix}
					{error.message}
				</li>
			))}
		</ul>
	);
}

// DashboardEditor is the runtime authoring surface: a structural editor bound to
// useDashboardAuthoring on the left and the live <Dashboard> canvas on the
// right. Every edit commits through authoring.setDashboard — validated against
// the live audit registry — into the localStorage-backed draft, so a valid edit
// repaints the canvas at once and survives a reload, while a rejected one
// surfaces inline and leaves the canvas on its last good spec.
export function DashboardEditor() {
	const { authoring, draft } = useDashboardAuthoring(STORAGE_KEY, INITIAL);

	const [state, setState] = useState<EditorState>(() => seedState(draft.spec));
	const [vocab, setVocab] = useState<EventTypeVocabulary | null>(null);
	const [commitErrors, setCommitErrors] = useState<FieldError[]>([]);
	const [previews, setPreviews] = useState<
		Record<string, PreviewResult | "loading">
	>({});

	// Tracks the spec we last put into the draft (a commit or an adopted external
	// change), so the effect below can tell the draft moving under us — a reload
	// restore or a cross-tab edit — from our own commit echoing back.
	const committedRef = useRef(JSON.stringify(draft.spec));
	const commitSeq = useRef(0);
	const nextId = useRef(state.rows.length);

	useEffect(() => {
		let active = true;
		authoring.listEventTypes().then((result) => {
			if (active) setVocab(result);
		});
		return () => {
			active = false;
		};
	}, [authoring]);

	// Adopt a draft that changed outside the editor: the persisted spec restored
	// on mount (reload survival), a cross-tab edit, or a reset. Our own commits
	// echo back with a matching signature and are ignored, so an in-progress
	// invalid edit is never clobbered.
	useEffect(() => {
		const incoming = JSON.stringify(draft.spec);
		if (incoming === committedRef.current) return;
		committedRef.current = incoming;
		const seeded = seedState(draft.spec);
		nextId.current = seeded.rows.length;
		setState(seeded);
	}, [draft.spec]);

	const commit = useCallback(
		(spec: DashboardDef) => {
			const mine = ++commitSeq.current;
			authoring.setDashboard(spec).then((result) => {
				if (result.ok) committedRef.current = JSON.stringify(spec);
				if (mine === commitSeq.current) {
					setCommitErrors(result.ok ? [] : result.errors);
				}
			});
		},
		[authoring],
	);

	// Every mutation flows through here: update the row-keyed state and commit
	// the derived spec, so the canvas follows a valid edit live.
	const apply = useCallback(
		(next: EditorState) => {
			setState(next);
			commit(deriveSpec(next));
		},
		[commit],
	);

	const setMetric = useCallback(
		(id: string, metric: MetricDef) =>
			apply({
				...state,
				rows: state.rows.map((row) =>
					row.id === id ? { ...row, metric } : row,
				),
			}),
		[apply, state],
	);

	const addWidget = useCallback(() => {
		const id = `w${nextId.current++}`;
		apply({
			...state,
			rows: [...state.rows, { id, metric: newWidget(state.rows.length) }],
		});
	}, [apply, state]);

	const removeWidget = useCallback(
		(id: string) =>
			apply({ ...state, rows: state.rows.filter((row) => row.id !== id) }),
		[apply, state],
	);

	const moveWidget = useCallback(
		(id: string, delta: -1 | 1) => {
			const index = state.rows.findIndex((row) => row.id === id);
			const target = index + delta;
			if (target < 0 || target >= state.rows.length) return;
			const rows = [...state.rows];
			[rows[index], rows[target]] = [rows[target], rows[index]];
			apply({ ...state, rows });
		},
		[apply, state],
	);

	const preview = useCallback(
		(id: string, metric: MetricDef) => {
			setPreviews((prev) => ({ ...prev, [id]: "loading" }));
			authoring.previewMetric(metric).then((result) => {
				setPreviews((prev) => ({ ...prev, [id]: result }));
			});
		},
		[authoring],
	);

	const reset = useCallback(() => {
		setPreviews({});
		draft.reset();
	}, [draft]);

	return (
		<div className="grid gap-6 lg:grid-cols-2">
			<div className="space-y-4">
				<Card>
					<CardHeader className="flex-row items-center justify-between space-y-0">
						<h2 className="text-lg font-semibold tracking-tight">Editor</h2>
						<Button variant="outline" size="sm" onClick={reset}>
							Reset
						</Button>
					</CardHeader>
					<CardContent className="space-y-4">
						<div className="space-y-2">
							<Label htmlFor="dashboard-title">Title</Label>
							<Input
								id="dashboard-title"
								value={state.title}
								onChange={(event) =>
									apply({ ...state, title: event.target.value })
								}
							/>
						</div>
						<div className="space-y-2">
							<Label htmlFor="dashboard-description">Description</Label>
							<Input
								id="dashboard-description"
								value={state.description}
								onChange={(event) =>
									apply({ ...state, description: event.target.value })
								}
							/>
						</div>
						<div className="space-y-2">
							<Label htmlFor="dashboard-columns">Columns</Label>
							<Select
								value={String(state.columns)}
								onValueChange={(value) =>
									apply({
										...state,
										columns: Number(value) as 1 | 2 | 3 | 4,
									})
								}
							>
								<SelectTrigger id="dashboard-columns">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{[1, 2, 3, 4].map((count) => (
										<SelectItem key={count} value={String(count)}>
											{count}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
						{commitErrors.length > 0 && errorList(commitErrors)}
						{draft.error && (
							<p className="text-sm text-destructive">{draft.error.message}</p>
						)}
					</CardContent>
				</Card>

				{state.rows.map((row, index) => (
					<WidgetEditor
						key={row.id}
						metric={row.metric}
						vocab={vocab}
						preview={previews[row.id]}
						isFirst={index === 0}
						isLast={index === state.rows.length - 1}
						onChange={(metric) => setMetric(row.id, metric)}
						onRemove={() => removeWidget(row.id)}
						onMove={(delta) => moveWidget(row.id, delta)}
						onPreview={() => preview(row.id, row.metric)}
					/>
				))}

				<Button variant="outline" onClick={addWidget} className="w-full">
					<Plus className="mr-2 h-4 w-4" />
					Add widget
				</Button>
			</div>

			<div className="lg:sticky lg:top-6 lg:self-start">
				<Dashboard data={draft.spec} />
			</div>
		</div>
	);
}

function WidgetEditor({
	metric,
	vocab,
	preview,
	isFirst,
	isLast,
	onChange,
	onRemove,
	onMove,
	onPreview,
}: {
	metric: MetricDef;
	vocab: EventTypeVocabulary | null;
	preview: PreviewResult | "loading" | undefined;
	isFirst: boolean;
	isLast: boolean;
	onChange: (metric: MetricDef) => void;
	onRemove: () => void;
	onMove: (delta: -1 | 1) => void;
	onPreview: () => void;
}) {
	const op = opOf(metric);
	const groupBy = Array.isArray(metric.groupBy) ? "event_type" : metric.groupBy;

	return (
		<Card>
			<CardHeader className="flex-row items-center gap-2 space-y-0">
				<GripVertical className="h-4 w-4 text-muted-foreground" />
				<Input
					aria-label="Widget title"
					value={metric.title}
					onChange={(event) =>
						onChange({ ...metric, title: event.target.value })
					}
				/>
				<Button
					variant="ghost"
					size="sm"
					aria-label="Move widget up"
					disabled={isFirst}
					onClick={() => onMove(-1)}
				>
					↑
				</Button>
				<Button
					variant="ghost"
					size="sm"
					aria-label="Move widget down"
					disabled={isLast}
					onClick={() => onMove(1)}
				>
					↓
				</Button>
				<Button
					variant="ghost"
					size="sm"
					aria-label="Remove widget"
					onClick={onRemove}
				>
					<Trash2 className="h-4 w-4" />
				</Button>
			</CardHeader>
			<CardContent className="space-y-4">
				<div className="grid grid-cols-2 gap-3">
					<Field label="Scope">
						<Select
							value={scopeValue(metric)}
							onValueChange={(value) =>
								onChange(withScope(metric, value as string))
							}
						>
							<SelectTrigger aria-label="Scope">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="all">All events</SelectItem>
								{vocab?.events.map((eventType) => (
									<SelectItem
										key={eventType.name}
										value={`event:${eventType.name}`}
									>
										{eventType.name}
									</SelectItem>
								))}
								{vocab?.categories.map((category) => (
									<SelectItem key={category} value={`category:${category}`}>
										Category: {category}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</Field>
					<Field label="Group by">
						<Select
							value={groupBy}
							onValueChange={(value) =>
								onChange(withGroupBy(metric, value as Dimension))
							}
						>
							<SelectTrigger aria-label="Group by">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{GROUP_BY.map((dimension) => (
									<SelectItem key={dimension} value={dimension}>
										{GROUP_BY_LABEL[dimension]}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</Field>
					<Field label="Chart">
						<Select
							value={metric.chart}
							onValueChange={(value) =>
								onChange({ ...metric, chart: value as ChartKind })
							}
						>
							<SelectTrigger aria-label="Chart">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{CHART.map((chart) => (
									<SelectItem key={chart} value={chart}>
										{chart}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</Field>
					<Field label="Metric">
						<Select
							value={op}
							onValueChange={(value) =>
								onChange(withOp(metric, value as FieldOp | "count"))
							}
						>
							<SelectTrigger aria-label="Metric">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{OP.map((option) => (
									<SelectItem key={option} value={option}>
										{option}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</Field>
					{groupBy === "time" && (
						<Field label="Bucket">
							<Select
								value={metric.bucket ?? "day"}
								onValueChange={(value) =>
									onChange({ ...metric, bucket: value as Bucket })
								}
							>
								<SelectTrigger aria-label="Bucket">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{BUCKET.map((bucket) => (
										<SelectItem key={bucket} value={bucket}>
											{bucket}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</Field>
					)}
					{metric.value && metric.value.op !== "count" && (
						<Field label="Field">
							<Input
								aria-label="Field"
								placeholder="payload:duration_ms"
								value={metric.value.field}
								onChange={(event) =>
									onChange(withField(metric, event.target.value))
								}
							/>
						</Field>
					)}
				</div>

				<Separator />

				<div className="flex items-center justify-between gap-4">
					<Button variant="secondary" size="sm" onClick={onPreview}>
						Preview
					</Button>
					<PreviewResultView preview={preview} />
				</div>
			</CardContent>
		</Card>
	);
}

function Field({
	label,
	children,
}: {
	label: string;
	children: React.ReactNode;
}) {
	return (
		<div className="space-y-1.5">
			<Label className="text-xs text-muted-foreground">{label}</Label>
			{children}
		</div>
	);
}

function PreviewResultView({
	preview,
}: {
	preview: PreviewResult | "loading" | undefined;
}) {
	if (preview === undefined) return null;
	if (preview === "loading") {
		return <span className="text-sm text-muted-foreground">Previewing…</span>;
	}
	if (preview.ok) {
		return (
			<span className="text-sm text-muted-foreground">
				Total {preview.preview.total.toLocaleString()} over{" "}
				{preview.preview.points.length} point
				{preview.preview.points.length === 1 ? "" : "s"}
			</span>
		);
	}
	if (preview.kind === "pending") {
		return (
			<span className="text-sm text-muted-foreground">{preview.message}</span>
		);
	}
	return <div className="flex-1">{errorList(preview.errors)}</div>;
}
