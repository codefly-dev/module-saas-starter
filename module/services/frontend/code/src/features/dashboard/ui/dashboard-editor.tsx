"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { ArrowDown, ArrowUp, Trash2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { AuditEventTypeInfo } from "@/features/audit";
import { formatAuditAction } from "@/features/audit/model/transforms";
import { useAuth } from "@/lib/auth";
import { failureText } from "@/shared/lib/failure-text";
import { readOutcome } from "@/shared/lib/read-outcome";
import {
	AlertDialog,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	Button,
	Card,
	CardContent,
	CardHeader,
	CardTitle,
	DelayedLoading,
	ErrorState,
	Field,
	Input,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Separator,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { metricIdentity } from "../model/identity";
import type {
	Bucket,
	ChartKind,
	DashboardDef,
	Dimension,
	MetricDef,
} from "../model/schema";
import type { FieldError } from "../model/validate";
import type { DashboardDraftStore } from "../service/draft-store";
import {
	scopedDashboardDraftKey,
	useDashboardAuthoring,
} from "../service/use-dashboard-authoring";
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
	const subject =
		form.eventType === ALL_EVENTS
			? "Events"
			: formatAuditAction(form.eventType);
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
	store,
}: {
	storageKey: string;
	initial: DashboardDef;
	// When set, edits persist through this store instead of localStorage — a
	// named dashboard record isolates itself by id, so the viewer/org key scoping
	// below (which guards a shared, unscoped browser key) is neither needed nor
	// applied on that path.
	store?: DashboardDraftStore;
}) {
	const { user, organizationId } = useAuth();
	const scopedKey = store
		? storageKey
		: scopedDashboardDraftKey(storageKey, {
				organizationId,
				userId: user?.id,
			});
	const { authoring, draft } = useDashboardAuthoring(scopedKey, initial, store);
	const spec = draft.spec;

	const [vocabularyError, setVocabularyError] = useState<unknown>();
	const [vocabularyLoading, setVocabularyLoading] = useState(true);
	const [reload, setReload] = useState(0);
	const [working, setWorking] = useState(false);
	const busy = useRef(false);
	const [actionError, setActionError] = useState<string>();
	const [removeTarget, setRemoveTarget] = useState<MetricDef | null>(null);
	const [events, setEvents] = useState<AuditEventTypeInfo[]>([]);
	const [form, setForm] = useState<WidgetForm>(DEFAULT_FORM);
	const [errors, setErrors] = useState<FieldError[]>([]);
	const [pending, setPending] = useState<string | null>(null);
	const [preview, setPreview] = useState<{
		total: number | null;
		points: number;
	} | null>(null);

	const [vocabularyRequest, setVocabularyRequest] = useState({ authoring, reload });
	if (vocabularyRequest.authoring !== authoring || vocabularyRequest.reload !== reload) {
		setVocabularyRequest({ authoring, reload });
		setVocabularyLoading(true);
		setVocabularyError(undefined);
	}

	useEffect(() => {
		let active = true;
		authoring
			.listEventTypes()
			.then((vocab) => {
				if (active) {
					setEvents(vocab.events);
					setVocabularyLoading(false);
				}
			})
			.catch((error) => {
				if (active) {
					setVocabularyError(error);
					setVocabularyLoading(false);
				}
			});
		return () => {
			active = false;
		};
	}, [authoring, reload]);

	// Commit a whole spec through the authoring contract so persistence and the
	// vocabulary check share one path. Returns whether it committed, so a caller
	// can clear its form only on success.
	const commit = useCallback(
		async (next: DashboardDef): Promise<boolean> => {
			if (busy.current) return false;
			busy.current = true;
			setWorking(true);
			setActionError(undefined);
			try {
				const result = await authoring.setDashboard(next);
				if (!result.ok) {
					setErrors(result.errors);
					return false;
				}
				setErrors([]);
				return true;
			} catch (error) {
				setActionError(failureText("save the dashboard", error));
				return false;
			} finally {
				busy.current = false;
				setWorking(false);
			}
		},
		[authoring],
	);

	const onPreview = useCallback(async () => {
		if (busy.current || vocabularyLoading || vocabularyError) return;
		busy.current = true;
		setWorking(true);
		setActionError(undefined);
		try {
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
				setPending("Select an organization to preview this widget.");
				return;
			}
			setErrors(result.errors);
		} catch (error) {
			setPreview(null);
			setActionError(failureText("preview the widget", error));
		} finally {
			busy.current = false;
			setWorking(false);
		}
	}, [authoring, form, vocabularyLoading, vocabularyError]);

	const onAdd = useCallback(async () => {
		if (busy.current || vocabularyLoading || vocabularyError) return;
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
	}, [commit, form, spec, vocabularyLoading, vocabularyError]);

	const onRemove = useCallback(
		async (index: number) => {
			return commit({
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
					<DelayedLoading
						active={vocabularyLoading}
						label="Loading available events"
					/>
					{vocabularyError !== undefined && (
						<ErrorState
							title="Couldn’t load available events"
							detail={failureText("load available events", vocabularyError)}
						/>
					)}
					{vocabularyError !== undefined &&
						readOutcome(true, vocabularyError) !== "forbidden" && (
							<Button
								variant="outline"
								onClick={() => setReload((value) => value + 1)}
							>
								Try again
							</Button>
						)}
					<Field label="Event">
						{(control) => (
							<>
								<Select
									disabled={working || vocabularyLoading || !!vocabularyError}
									value={form.eventType}
									items={[
										{ value: ALL_EVENTS, label: "All events" },
										...events.map((entry) => ({
											value: entry.name,
											label: formatAuditAction(entry.name),
										})),
									]}
									onValueChange={(value) =>
										update("eventType", value ?? ALL_EVENTS)
									}
								>
									<SelectTrigger {...control} aria-label="Event">
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										<SelectItem value={ALL_EVENTS}>All events</SelectItem>
										{events.map((entry) => (
											<SelectItem key={entry.name} value={entry.name}>
												{formatAuditAction(entry.name)}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							</>
						)}
					</Field>

					<Field label="Group by">
						{(control) => (
							<>
								<Select
									disabled={working || vocabularyLoading || !!vocabularyError}
									value={form.groupBy}
									items={GROUP_BY_OPTIONS}
									onValueChange={(value) => {
										if (value) update("groupBy", value as Dimension);
									}}
								>
									<SelectTrigger {...control} aria-label="Group by">
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
							</>
						)}
					</Field>

					{form.groupBy === "time" && (
						<Field label="Interval">
							{(control) => (
								<>
									<Select
										disabled={working || vocabularyLoading || !!vocabularyError}
										value={form.bucket}
										items={BUCKET_OPTIONS}
										onValueChange={(value) => {
											if (value) update("bucket", value as Bucket);
										}}
									>
										<SelectTrigger {...control} aria-label="Interval">
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
								</>
							)}
						</Field>
					)}

					<Field label="Chart">
						{(control) => (
							<>
								<Select
									disabled={working || vocabularyLoading || !!vocabularyError}
									value={form.chart}
									items={CHART_OPTIONS}
									onValueChange={(value) => {
										if (value) update("chart", value as ChartKind);
									}}
								>
									<SelectTrigger {...control} aria-label="Chart">
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
							</>
						)}
					</Field>

					<Field label="Title">
						{(control) => (
							<>
								<Input
									disabled={working}
									{...control}
									value={form.title}
									placeholder={autoTitle(form)}
									onChange={(e) => update("title", e.target.value)}
								/>
							</>
						)}
					</Field>

					<div className="flex gap-2">
						<Button
							type="button"
							variant="outline"
							onClick={onPreview}
							disabled={working || vocabularyLoading || !!vocabularyError}
						>
							Preview
						</Button>
						<Button
							type="button"
							onClick={onAdd}
							disabled={working || vocabularyLoading || !!vocabularyError}
						>
							Add widget
						</Button>
					</div>

					{preview && (
						<p className="text-sm text-muted-foreground" role="status">
							Preview: {preview.total ?? "total unavailable"} across{" "}
							{preview.points} point
							{preview.points === 1 ? "" : "s"}.
						</p>
					)}
					{pending && (
						<p className="text-sm text-muted-foreground" role="status">
							{pending}
						</p>
					)}
					{actionError && (
						<ErrorState title="Dashboard change failed" detail={actionError} />
					)}
					{(errors.length > 0 || draft.error) && (
						<ErrorState
							title="Dashboard wasn’t saved"
							detail={
								errors.some((error) => error.code === "duplicate_metric")
									? "This dashboard already shows that widget. Change the event or grouping to add a different one."
									: "Review the widget settings and available events. Your saved dashboard has not been replaced."
							}
						/>
					)}
				</CardContent>
			</Card>

			<div className="space-y-4">
				<Card>
					<CardHeader>
						<CardTitle>Widgets</CardTitle>
					</CardHeader>
					<CardContent className="space-y-2">
						<WidgetTable
							metrics={spec.metrics}
							working={working}
							onMove={onMove}
							onRemove={(metric) => {
								setActionError(undefined);
								setErrors([]);
								setRemoveTarget(metric);
							}}
						/>
					</CardContent>
				</Card>

				<AlertDialog
					open={!!removeTarget}
					onOpenChange={(next) => {
						if (!next && !busy.current) setRemoveTarget(null);
					}}
				>
					<AlertDialogContent>
						<AlertDialogHeader>
							<AlertDialogTitle>
								Remove “{removeTarget?.title}”?
							</AlertDialogTitle>
							<AlertDialogDescription>
								This removes the saved widget from this dashboard.
							</AlertDialogDescription>
						</AlertDialogHeader>
						{(actionError || errors.length > 0) && (
							<ErrorState
								title="Widget wasn’t removed"
								detail={
									actionError ??
									"The remaining widgets need attention. Review their event settings before saving this change."
								}
							/>
						)}
						<AlertDialogFooter>
							<AlertDialogCancel disabled={working}>Cancel</AlertDialogCancel>
							<Button
								variant="destructive"
								disabled={working}
								onClick={async () => {
									if (!removeTarget || busy.current) return;
									const index = spec.metrics.findIndex(
										(metric) =>
											metricIdentity(metric) === metricIdentity(removeTarget),
									);
									if (index >= 0 && (await onRemove(index)))
										setRemoveTarget(null);
								}}
							>
								{working ? "Removing…" : "Remove widget"}
							</Button>
						</AlertDialogFooter>
					</AlertDialogContent>
				</AlertDialog>
				<Separator />

				<Dashboard data={spec} />
			</div>
		</div>
	);
}

const widgetColumn = createColumnHelper<MetricDef>();
function WidgetTable({
	metrics,
	working,
	onMove,
	onRemove,
}: {
	metrics: MetricDef[];
	working: boolean;
	onMove: (index: number, delta: -1 | 1) => Promise<void>;
	onRemove: (metric: MetricDef) => void;
}) {
	const columns = useMemo(
		() => [
			widgetColumn.accessor("title", { header: "Widget" }),
			widgetColumn.display({
				id: "actions",
				header: "",
				cell: ({ row }) => {
					const metric = row.original;
					const index = row.index;
					return (
						<div className="flex shrink-0 gap-1">
							<Button
								type="button"
								variant="ghost"
								size="icon"
								aria-label={`Move "${metric.title}" up`}
								disabled={working || index === 0}
								onClick={() => onMove(index, -1)}
							>
								<ArrowUp className="size-4" />
							</Button>
							<Button
								type="button"
								variant="ghost"
								size="icon"
								aria-label={`Move "${metric.title}" down`}
								disabled={working || index === metrics.length - 1}
								onClick={() => onMove(index, 1)}
							>
								<ArrowDown className="size-4" />
							</Button>
							<Button
								type="button"
								variant="ghost"
								size="icon"
								aria-label={`Remove "${metric.title}"`}
								disabled={working}
								onClick={() => onRemove(metric)}
							>
								<Trash2 className="size-4" />
							</Button>
						</div>
					);
				},
			}),
		],
		[metrics.length, working, onMove, onRemove],
	);
	const table = useReactTable({
		data: metrics,
		columns,
		getCoreRowModel: getCoreRowModel(),
	});
	return (
		<DataTable
			table={table}
			emptyMessage="No widgets yet. Add one from the panel to start your dashboard."
		/>
	);
}
