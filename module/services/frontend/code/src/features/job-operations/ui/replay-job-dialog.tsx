"use client";

import { toast } from "sonner";
import type { JobSummary } from "@/gen/saas/jobs/v1/jobs_pb";
import { failureText } from "@/shared/lib/failure-text";
import { formatDate } from "@/shared/lib/utils";
import { ConfirmDialog } from "@/shared/ui";
import { useReplayJob } from "../service/mutations";

/**
 * The question before a failed job is run again. Queueing cannot be taken
 * back, so the dialog names the job and says what happens to the old one.
 */
export function ReplayJobDialog({
	job,
	onClose,
	onReplayed,
}: {
	/** The job to run again; `null` keeps the dialog closed. */
	job: JobSummary | null;
	onClose: () => void;
	onReplayed: () => void;
}) {
	const replay = useReplayJob();
	return (
		<ConfirmDialog
			open={job !== null}
			onOpenChange={(open) => !open && onClose()}
			title={
				job
					? `Run the ${job.topic} job from ${formatDate(job.createdAt)} again?`
					: "Run this job again?"
			}
			description="This queues a new copy of the job straight away, and a queued job cannot be taken back. The job that failed and its history stay as they are. You may be asked to confirm your identity first."
			confirmLabel="Run job again"
			pendingLabel="Queueing…"
			destructive={false}
			isPending={replay.isPending}
			onConfirm={() => {
				if (!job) return;
				replay.mutate(job.id, {
					onSuccess: () => {
						toast.success("Job queued again", {
							description: "A new copy of the job is waiting to start.",
						});
						onReplayed();
					},
					onError: (error) =>
						toast.error(failureText("run the job again", error)),
				});
			}}
		/>
	);
}
