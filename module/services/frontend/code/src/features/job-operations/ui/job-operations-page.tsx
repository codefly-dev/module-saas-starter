"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { AlertTriangle, ListChecks, RefreshCw, RotateCcw } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import type { JobSummary } from "@/gen/saas/jobs/v1/jobs_pb";
import { JobState } from "@/gen/saas/jobs/v1/jobs_pb";
import { formatDate, truncateUUID } from "@/shared/lib/utils";
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
	Input,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Skeleton,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "@/shared/ui";
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
			[JobState.DEAD_LETTER]: "Dead letter",
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

function scopeLabel(job: JobSummary): string {
	switch (job.scope?.value.case) {
		case "organizationId":
			return `org:${truncateUUID(job.scope.value.value)}`;
		case "subjectId":
			return `subject:${truncateUUID(job.scope.value.value)}`;
		case "global":
			return "global";
		default:
			return "-";
	}
}

function count(values: bigint[]): string {
	return values
		.reduce((total, value) => total + value, BigInt(0))
		.toLocaleString();
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
		<div className="space-y-6">
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div>
					<h1 className="text-2xl font-bold tracking-tight">Job operations</h1>
					<p className="text-muted-foreground">
						Queue health and payload-free lifecycle history across the platform.
					</p>
				</div>
				<Button
					variant="outline"
					onClick={() => Promise.all([operations.refetch(), jobs.refetch()])}
					disabled={operations.isFetching || jobs.isFetching}
				>
					<RefreshCw className="h-4 w-4" />
					Refresh
				</Button>
			</div>

			<div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
				<MetricCard
					title="Ready"
					value={totals.ready}
					description="Claimable now"
				/>
				<MetricCard
					title="Processing"
					value={totals.processing}
					description="Active leases"
				/>
				<MetricCard
					title="Dead letter"
					value={totals.deadLetter}
					description="Operator review"
				/>
				<MetricCard
					title="Expired leases"
					value={totals.expired}
					description="Awaiting recovery"
					warning={totals.expired !== "0"}
				/>
			</div>

			<Card>
				<CardHeader>
					<CardTitle className="text-base">Queues</CardTitle>
					<CardDescription>
						Database-derived counts; queue is the only metric label.
					</CardDescription>
				</CardHeader>
				<CardContent>
					{operations.isLoading ? (
						<Skeleton className="h-24 w-full" />
					) : snapshots.length === 0 ? (
						<p className="text-sm text-muted-foreground">No queues found.</p>
					) : (
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Queue</TableHead>
									<TableHead>Ready</TableHead>
									<TableHead>Processing</TableHead>
									<TableHead>Scheduled</TableHead>
									<TableHead>Dead letter</TableHead>
									<TableHead>Oldest ready</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{snapshots.map((snapshot) => (
									<TableRow key={snapshot.queue}>
										<TableCell className="font-mono text-xs">
											{snapshot.queue}
										</TableCell>
										<TableCell>{snapshot.ready.toLocaleString()}</TableCell>
										<TableCell>
											{snapshot.processing.toLocaleString()}
										</TableCell>
										<TableCell>{snapshot.scheduled.toLocaleString()}</TableCell>
										<TableCell>
											{snapshot.deadLetter.toLocaleString()}
										</TableCell>
										<TableCell>
											{formatDate(
												snapshot.oldestReadyAt
													? timestampDate(snapshot.oldestReadyAt).toISOString()
													: undefined,
											)}
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					)}
				</CardContent>
			</Card>

			<Card>
				<CardHeader>
					<CardTitle className="text-base">Jobs</CardTitle>
					<CardDescription>
						Routing, lease, retry, and failure metadata only. Payloads and
						attributes are never returned.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					<div className="flex flex-wrap gap-2">
						<Input
							value={queueDraft}
							onChange={(event) => setQueueDraft(event.target.value)}
							onKeyDown={(event) => event.key === "Enter" && applyQueue()}
							placeholder="Filter queue"
							className="max-w-xs"
						/>
						<Button variant="outline" onClick={applyQueue}>
							Apply
						</Button>
						<Select
							value={state === undefined ? "all" : String(state)}
							onValueChange={selectState}
						>
							<SelectTrigger className="w-44">
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
						<div className="flex items-center gap-2 text-sm text-destructive">
							<AlertTriangle className="h-4 w-4" />
							Unable to load job metadata.
						</div>
					) : jobs.data?.jobs.length === 0 ? (
						<div className="flex flex-col items-center gap-2 py-10 text-muted-foreground">
							<ListChecks className="h-8 w-8" />
							<p className="text-sm">No jobs match these filters.</p>
						</div>
					) : (
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Created</TableHead>
									<TableHead>Queue / topic</TableHead>
									<TableHead>Scope</TableHead>
									<TableHead>State</TableHead>
									<TableHead>Attempts</TableHead>
									<TableHead>Failure</TableHead>
									<TableHead />
								</TableRow>
							</TableHeader>
							<TableBody>
								{jobs.data?.jobs.map((job) => (
									<TableRow key={job.id}>
										<TableCell>
											{formatDate(timestamp(job.createdAt))}
										</TableCell>
										<TableCell>
											<div className="font-mono text-xs">{job.queue}</div>
											<div className="text-xs text-muted-foreground">
												{job.topic}
											</div>
										</TableCell>
										<TableCell className="font-mono text-xs">
											{scopeLabel(job)}
										</TableCell>
										<TableCell>
											<Badge variant={stateBadgeVariant(job.state)}>
												{jobStateLabel(job.state)}
											</Badge>
										</TableCell>
										<TableCell>
											{job.attemptCount}/{job.maxAttempts}
										</TableCell>
										<TableCell className="max-w-48 truncate text-xs text-muted-foreground">
											{job.lastFailure?.code ?? "-"}
										</TableCell>
										<TableCell>
											<Button
												size="sm"
												variant="ghost"
												onClick={() => setSelectedJobID(job.id)}
											>
												Inspect
											</Button>
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
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
							Payload-free immutable lifecycle metadata.
						</DialogDescription>
					</DialogHeader>
					{detail.isLoading || !detail.data?.job ? (
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
											<div
												key={attempt.id}
												className="rounded-md border p-3 text-xs"
											>
												<div className="flex justify-between">
													<span>
														Attempt {attempt.number} · {attempt.workerId}
													</span>
													<span>{attempt.failure?.code ?? "in progress"}</span>
												</div>
												<div className="mt-1 text-muted-foreground">
													Started{" "}
													{formatDate(
														attempt.startedAt
															? timestampDate(attempt.startedAt).toISOString()
															: undefined,
													)}
												</div>
											</div>
										))}
									</div>
								)}
							</div>
							<div>
								<h3 className="mb-2 font-medium">State history</h3>
								<div className="space-y-2">
									{detail.data.transitions.map((transition) => (
										<div
											key={transition.sequence.toString()}
											className="flex justify-between rounded-md border p-3 text-xs"
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
										</div>
									))}
								</div>
							</div>
							{detail.data.job.state === JobState.DEAD_LETTER && (
								<Button
									variant="destructive"
									onClick={() => setReplayTarget(detail.data?.job ?? null)}
								>
									<RotateCcw className="h-4 w-4" />
									Replay dead-lettered job
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
						<AlertDialogTitle>Replay this job?</AlertDialogTitle>
						<AlertDialogDescription>
							This creates a new pending job linked to the dead-lettered source.
							It does not rewrite history. The operation requires a recent MFA
							step-up.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
							disabled={replay.isPending}
							onClick={() =>
								replayTarget &&
								replay.mutate(replayTarget.id, {
									onSuccess: (response) => {
										toast.success("Job replayed", {
											description: `New job ${truncateUUID(response.jobId)}`,
										});
										setReplayTarget(null);
										setSelectedJobID(null);
									},
									onError: (error) =>
										toast.error("Replay failed", {
											description: error.message,
										}),
								})
							}
						>
							{replay.isPending ? "Replaying…" : "Replay"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
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
		<dl className="grid gap-3 rounded-md border p-4 text-sm sm:grid-cols-2">
			<div>
				<dt className="text-muted-foreground">ID</dt>
				<dd className="break-all font-mono text-xs">{job.id}</dd>
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
				<dt className="text-muted-foreground">Idempotency key</dt>
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
				<dt className="text-muted-foreground">Content type / schema</dt>
				<dd>
					{job.contentType} · v{job.schemaVersion}
				</dd>
			</div>
			{job.replayOf && (
				<div className="sm:col-span-2">
					<dt className="text-muted-foreground">Replay of</dt>
					<dd className="font-mono text-xs">{job.replayOf}</dd>
				</div>
			)}
			{job.lastFailure && (
				<div className="sm:col-span-2">
					<dt className="text-muted-foreground">Last safe failure</dt>
					<dd>
						<span className="font-mono text-xs">{job.lastFailure.code}</span>
						{job.lastFailure.message && (
							<span className="ml-2 text-muted-foreground">
								{job.lastFailure.message}
							</span>
						)}
					</dd>
				</div>
			)}
		</dl>
	);
}
