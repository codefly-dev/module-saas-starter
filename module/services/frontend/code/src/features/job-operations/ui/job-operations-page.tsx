"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { EmptyState } from "@codefly-dev/ui/layout";
import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { ListChecks, RefreshCw, RotateCcw } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { ResourceLabel } from "@/components/resource-label";
import type { JobQueueSnapshot, JobSummary } from "@/gen/saas/jobs/v1/jobs_pb";
import { JobState } from "@/gen/saas/jobs/v1/jobs_pb";
import { failureText } from "@/shared/lib/failure-text";
import { readOutcome } from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	Badge,
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
	ErrorState,
	Input,
	Page as PageBody,
	PageHeader,
	Panel,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Skeleton,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { useReplayJob } from "../service/mutations";
import { useJob, useJobOperations, useJobs } from "../service/queries";

const stateOptions = [
	JobState.PENDING,
	JobState.PROCESSING,
	JobState.RETRYING,
	JobState.SUCCEEDED,
	JobState.DEAD_LETTER,
	JobState.CANCELED,
] as const;

export function jobStateLabel(state: JobState): string {
	return (
		{
			[JobState.PENDING]: "Pending",
			[JobState.PROCESSING]: "Processing",
			[JobState.RETRYING]: "Retrying",
			[JobState.SUCCEEDED]: "Succeeded",
			[JobState.DEAD_LETTER]: "Failed for good",
			[JobState.CANCELED]: "Canceled",
			[JobState.UNSPECIFIED]: "Unknown",
		} satisfies Record<JobState, string>
	)[state];
}

function stateBadgeVariant(state: JobState) {
	if (state === JobState.SUCCEEDED) return "default" as const;
	if (state === JobState.DEAD_LETTER) return "destructive" as const;
	if (state === JobState.PROCESSING || state === JobState.RETRYING)
		return "secondary" as const;
	return "outline" as const;
}

function timestamp(value: JobSummary["createdAt"]): string | undefined {
	return value ? timestampDate(value).toISOString() : undefined;
}

function scopeLabel(job: JobSummary) {
	switch (job.scope?.value.case) {
		case "organizationId":
			return (
				<ResourceLabel resource="organization" id={job.scope.value.value} />
			);
		case "subjectId":
			return <ResourceLabel resource="user" id={job.scope.value.value} />;
		case "global":
			return "Whole platform";
		default:
			return "-";
	}
}

function count(values: bigint[]): string {
	return values
		.reduce((total, value) => total + value, BigInt(0))
		.toLocaleString();
}

// What a person can do about a read that did not come back: nothing but ask,
// when it was refused; otherwise the Refresh button is right there.
function loadFailureDetail(error: unknown): string {
	return readOutcome(true, error) === "forbidden"
		? "You don’t have access to this page. Ask a platform administrator to grant it."
		: "Use Refresh to try again. If it keeps failing, tell your administrator.";
}

export function JobOperationsPage() {
	const [queueDraft, setQueueDraft] = useState("");
	const [queue, setQueue] = useState("");
	const [state, setState] = useState<JobState | undefined>();
	const [pageToken, setPageToken] = useState("");
	const [previousTokens, setPreviousTokens] = useState<string[]>([]);
	const [selectedJobID, setSelectedJobID] = useState<string | null>(null);
	const [replayTarget, setReplayTarget] = useState<JobSummary | null>(null);

	const operations = useJobOperations(queue);
	const jobs = useJobs({ queue, state, pageToken });
	const detail = useJob(selectedJobID);
	const replay = useReplayJob();
	const snapshots = useMemo(
		() => operations.data?.queues ?? [],
		[operations.data?.queues],
	);

	// Totals are sums over a read; with no answer there is nothing to sum, and
	// a row of zeros would read as four healthy queues.
	const counted = operations.data !== undefined && !operations.isError;
	const totals = useMemo(
		() => ({
			ready: count(snapshots.map((item) => item.ready)),
			processing: count(snapshots.map((item) => item.processing)),
			deadLetter: count(snapshots.map((item) => item.deadLetter)),
			expired: count(snapshots.map((item) => item.expiredLeases)),
		}),
		[snapshots],
	);

	function applyQueue() {
		setQueue(queueDraft.trim());
		setPageToken("");
		setPreviousTokens([]);
	}

	function selectState(value: string | null) {
		setState(value === "all" || value === null ? undefined : Number(value));
		setPageToken("");
		setPreviousTokens([]);
	}

	return (
		<PageBody>
			<PageHeader
				title="Job operations"
				description="How the platform's background work queues are doing, and the history of each job. What a job carries is never shown here."
				actions={
					<Button
						variant="outline"
						onClick={() => Promise.all([operations.refetch(), jobs.refetch()])}
						disabled={operations.isFetching || jobs.isFetching}
					>
						<RefreshCw className="h-4 w-4" />
						Refresh
					</Button>
				}
			/>

			<div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
				<MetricCard
					title="Ready"
					value={counted ? totals.ready : "—"}
					description="Waiting to start"
				/>
				<MetricCard
					title="Processing"
					value={counted ? totals.processing : "—"}
					description="Running now"
				/>
				<MetricCard
					title="Failed for good"
					value={counted ? totals.deadLetter : "—"}
					description="Needs someone to look"
				/>
				<MetricCard
					title="Stalled"
					value={counted ? totals.expired : "—"}
					description="Stopped mid-run, waiting to be picked up again"
					warning={counted && totals.expired !== "0"}
				/>
			</div>

			<Card>
				<CardHeader>
					<CardTitle role="heading" aria-level={2} className="text-base">
						Queues
					</CardTitle>
					<CardDescription>
						How many jobs each queue holds right now.
					</CardDescription>
				</CardHeader>
				<CardContent>
					{operations.isLoading ? (
						<Skeleton className="h-24 w-full" />
					) : operations.isError ? (
						<ErrorState
							title="Couldn't load the queues"
							detail={loadFailureDetail(operations.error)}
						/>
					) : snapshots.length === 0 ? (
						<EmptyState
							icon={<ListChecks />}
							className="py-6"
							heading="No queues found"
							description="Queues appear when the application schedules background work."
						/>
					) : (
						<QueueTable snapshots={snapshots} />
					)}
				</CardContent>
			</Card>

			<Card>
				<CardHeader>
					<CardTitle role="heading" aria-level={2} className="text-base">
						Jobs
					</CardTitle>
					<CardDescription>
						Where each job went, how many times it was tried and why it failed.
						What a job carries is never shown.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					<div className="flex flex-wrap gap-2">
						<Input
							value={queueDraft}
							onChange={(event) => setQueueDraft(event.target.value)}
							onKeyDown={(event) => event.key === "Enter" && applyQueue()}
							placeholder="Filter by queue"
							aria-label="Filter by queue"
							className="max-w-xs"
						/>
						<Button variant="outline" onClick={applyQueue}>
							Apply
						</Button>
						<Select
							value={state === undefined ? "all" : String(state)}
							items={[
								{ value: "all", label: "All states" },
								...stateOptions.map((option) => ({
									value: String(option),
									label: jobStateLabel(option),
								})),
							]}
							onValueChange={selectState}
						>
							<SelectTrigger className="w-44" aria-label="Job state">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="all">All states</SelectItem>
								{stateOptions.map((option) => (
									<SelectItem key={option} value={String(option)}>
										{jobStateLabel(option)}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>

					{jobs.isLoading ? (
						<Skeleton className="h-64 w-full" />
					) : jobs.isError ? (
						<ErrorState
							title="Couldn't load the jobs"
							detail={loadFailureDetail(jobs.error)}
						/>
					) : jobs.data?.jobs.length === 0 ? (
						<EmptyState
							icon={<ListChecks />}
							className="py-6"
							heading={
								queue || state !== undefined
									? "No jobs match these filters"
									: "No background jobs yet"
							}
							description={
								queue || state !== undefined
									? "Clear the filters to see all available background jobs."
									: "Jobs appear when the application schedules background work."
							}
						>
							{(queue || state !== undefined) && (
								<Button
									variant="outline"
									onClick={() => {
										setQueueDraft("");
										setQueue("");
										setState(undefined);
										setPageToken("");
										setPreviousTokens([]);
									}}
								>
									Clear filters
								</Button>
							)}
						</EmptyState>
					) : (
						<JobsTable
							jobs={jobs.data?.jobs ?? []}
							onInspect={setSelectedJobID}
						/>
					)}

					<div className="flex justify-end gap-2">
						<Button
							variant="outline"
							disabled={previousTokens.length === 0}
							onClick={() => {
								const prior = previousTokens.at(-1) ?? "";
								setPreviousTokens((tokens) => tokens.slice(0, -1));
								setPageToken(prior);
							}}
						>
							Previous
						</Button>
						<Button
							variant="outline"
							disabled={!jobs.data?.nextPageToken}
							onClick={() => {
								setPreviousTokens((tokens) => [...tokens, pageToken]);
								setPageToken(jobs.data?.nextPageToken ?? "");
							}}
						>
							Next
						</Button>
					</div>
				</CardContent>
			</Card>

			<Dialog
				open={selectedJobID !== null}
				onOpenChange={(open) => !open && setSelectedJobID(null)}
			>
				<DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
					<DialogHeader>
						<DialogTitle>Job details</DialogTitle>
						<DialogDescription>
							The job's history as it was recorded. What it carries is never
							shown.
						</DialogDescription>
					</DialogHeader>
					{detail.isError ? (
						<ErrorState
							title="Couldn't load this job"
							detail={
								readOutcome(true, detail.error) === "forbidden"
									? "You don’t have access to this job. Ask a platform administrator to grant it."
									: "Close this and open the job again. If it keeps failing, tell your administrator."
							}
						/>
					) : detail.isLoading || !detail.data?.job ? (
						<Skeleton className="h-64 w-full" />
					) : (
						<div className="space-y-5">
							<JobDetail job={detail.data.job} />
							<div>
								<h3 className="mb-2 font-medium">Attempts</h3>
								{detail.data.attempts.length === 0 ? (
									<p className="text-sm text-muted-foreground">
										No attempts yet.
									</p>
								) : (
									<div className="space-y-2">
										{detail.data.attempts.map((attempt) => (
											<Panel key={attempt.id} className="p-3 text-xs">
												<div className="flex justify-between">
													<span>
														Attempt {attempt.number} · {attempt.workerId}
													</span>
													<span>
														{attempt.failure
															? "Attempt failed"
															: attempt.finishedAt
																? "Finished"
																: "In progress"}
													</span>
												</div>
												<div className="mt-1 text-muted-foreground">
													Started{" "}
													{formatDate(
														attempt.startedAt
															? timestampDate(attempt.startedAt).toISOString()
															: undefined,
													)}
												</div>
											</Panel>
										))}
									</div>
								)}
							</div>
							<div>
								<h3 className="mb-2 font-medium">State history</h3>
								<div className="space-y-2">
									{detail.data.transitions.map((transition) => (
										<Panel
											key={transition.sequence.toString()}
											className="flex justify-between p-3 text-xs"
										>
											<span>
												{transition.fromState === JobState.UNSPECIFIED
													? "Created"
													: jobStateLabel(transition.fromState)}{" "}
												→ {jobStateLabel(transition.toState)}
											</span>
											<span className="text-muted-foreground">
												{formatDate(
													transition.occurredAt
														? timestampDate(transition.occurredAt).toISOString()
														: undefined,
												)}
											</span>
										</Panel>
									))}
								</div>
							</div>
							{detail.data.job.state === JobState.DEAD_LETTER && (
								<Button
									variant="destructive"
									onClick={() => setReplayTarget(detail.data?.job ?? null)}
								>
									<RotateCcw className="h-4 w-4" />
									Run this job again
								</Button>
							)}
						</div>
					)}
				</DialogContent>
			</Dialog>

			<AlertDialog
				open={replayTarget !== null}
				onOpenChange={(open) => !open && setReplayTarget(null)}
			>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Run this job again?</AlertDialogTitle>
						<AlertDialogDescription>
							This queues a new job linked to the one that failed. The failed
							job and its history stay as they are. You may be asked to confirm
							your identity first.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
							disabled={replay.isPending}
							onClick={() =>
								replayTarget &&
								replay.mutate(replayTarget.id, {
									onSuccess: () => {
										toast.success("Job queued again", {
											description: "A new job has been queued.",
										});
										setReplayTarget(null);
										setSelectedJobID(null);
									},
									onError: (error) =>
										toast.error(failureText("run the job again", error)),
								})
							}
						>
							{replay.isPending ? "Queueing…" : "Run again"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</PageBody>
	);
}

function QueueTable({ snapshots }: { snapshots: JobQueueSnapshot[] }) {
	const columns = useMemo(() => {
		const col = createColumnHelper<JobQueueSnapshot>();
		return [
			col.accessor("queue", { header: "Queue" }),
			col.accessor("ready", {
				header: "Ready",
				cell: ({ getValue }) => getValue().toLocaleString(),
			}),
			col.accessor("processing", {
				header: "Processing",
				cell: ({ getValue }) => getValue().toLocaleString(),
			}),
			col.accessor("scheduled", {
				header: "Scheduled",
				cell: ({ getValue }) => getValue().toLocaleString(),
			}),
			col.accessor("deadLetter", {
				header: "Failed for good",
				cell: ({ getValue }) => getValue().toLocaleString(),
			}),
			col.accessor("oldestReadyAt", {
				header: "Oldest ready",
				cell: ({ getValue }) => formatDate(timestamp(getValue())),
			}),
		];
	}, []);
	const table = useReactTable({
		data: snapshots,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});
	return <DataTable table={table} />;
}

function JobsTable({
	jobs,
	onInspect,
}: {
	jobs: JobSummary[];
	onInspect: (id: string) => void;
}) {
	const columns = useMemo(() => {
		const col = createColumnHelper<JobSummary>();
		return [
			col.accessor("createdAt", {
				header: "Created",
				cell: ({ getValue }) => formatDate(timestamp(getValue())),
			}),
			col.accessor("queue", {
				header: "Queue / topic",
				cell: ({ row }) => (
					<div>
						{row.original.queue}
						<p className="text-xs text-muted-foreground">
							{row.original.topic}
						</p>
					</div>
				),
			}),
			col.display({
				id: "scope",
				header: "Applies to",
				cell: ({ row }) => scopeLabel(row.original),
			}),
			col.accessor("state", {
				header: "State",
				cell: ({ getValue }) => (
					<Badge variant={stateBadgeVariant(getValue())}>
						{jobStateLabel(getValue())}
					</Badge>
				),
			}),
			col.display({
				id: "attempts",
				header: "Attempts",
				cell: ({ row }) =>
					`${row.original.attemptCount}/${row.original.maxAttempts}`,
			}),
			col.display({
				id: "failure",
				header: "Failure",
				cell: ({ row }) =>
					row.original.lastFailure ? "Attempt failed" : "None recorded",
			}),
			col.display({
				id: "inspect",
				header: "Details",
				cell: ({ row }) => (
					<Button
						size="sm"
						variant="ghost"
						onClick={() => onInspect(row.original.id)}
					>
						Inspect
					</Button>
				),
			}),
		];
	}, [onInspect]);
	const table = useReactTable({
		data: jobs,
		columns,
		getCoreRowModel: getCoreRowModel(),
		manualPagination: true,
		pageCount: 1,
	});
	return <DataTable table={table} />;
}

function MetricCard({
	title,
	value,
	description,
	warning = false,
}: {
	title: string;
	value: string;
	description: string;
	warning?: boolean;
}) {
	return (
		<Card>
			<CardHeader className="pb-2">
				<CardDescription>{title}</CardDescription>
				<CardTitle className={warning ? "text-destructive" : undefined}>
					{value}
				</CardTitle>
			</CardHeader>
			<CardContent className="text-xs text-muted-foreground">
				{description}
			</CardContent>
		</Card>
	);
}

function JobDetail({ job }: { job: JobSummary }) {
	return (
		<Panel>
			<dl className="grid gap-3 text-sm sm:grid-cols-2">
				<div>
					<dt className="text-muted-foreground">Reference</dt>
					<dd>
						<details>
							<summary className="cursor-pointer">Show the job's id</summary>
							<code className="break-all text-xs">{job.id}</code>
						</details>
					</dd>
				</div>
				<div>
					<dt className="text-muted-foreground">State</dt>
					<dd>
						<Badge variant={stateBadgeVariant(job.state)}>
							{jobStateLabel(job.state)}
						</Badge>
					</dd>
				</div>
				<div>
					<dt className="text-muted-foreground">Queue</dt>
					<dd className="font-mono text-xs">{job.queue}</dd>
				</div>
				<div>
					<dt className="text-muted-foreground">Topic</dt>
					<dd className="font-mono text-xs">{job.topic}</dd>
				</div>
				<div>
					<dt className="text-muted-foreground">Source</dt>
					<dd className="font-mono text-xs">{job.source}</dd>
				</div>
				<div>
					<dt className="text-muted-foreground">Scope</dt>
					<dd className="font-mono text-xs">{scopeLabel(job)}</dd>
				</div>
				<div>
					<dt className="text-muted-foreground">Duplicate-protection key</dt>
					<dd className="break-all font-mono text-xs">{job.idempotencyKey}</dd>
				</div>
				<div>
					<dt className="text-muted-foreground">Ordering key</dt>
					<dd className="break-all font-mono text-xs">
						{job.orderingKey || "-"}
					</dd>
				</div>
				<div>
					<dt className="text-muted-foreground">Attempts</dt>
					<dd>
						{job.attemptCount}/{job.maxAttempts}
					</dd>
				</div>
				<div>
					<dt className="text-muted-foreground">Format and version</dt>
					<dd>
						{job.contentType} · v{job.schemaVersion}
					</dd>
				</div>
				{job.replayOf && (
					<div className="sm:col-span-2">
						<dt className="text-muted-foreground">Re-run of</dt>
						<dd className="font-mono text-xs">{job.replayOf}</dd>
					</div>
				)}
				{job.lastFailure && (
					<div className="sm:col-span-2">
						<dt className="text-muted-foreground">Last failure</dt>
						<dd>
							The job could not finish. Review its attempts and ask a platform
							administrator to investigate before running it again.
						</dd>
					</div>
				)}
			</dl>
		</Panel>
	);
}
