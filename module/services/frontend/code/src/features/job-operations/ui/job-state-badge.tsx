import type { JobState } from "@/gen/saas/jobs/v1/jobs_pb";
import { Badge } from "@/shared/ui";
import { jobStateLabel, jobStateTone } from "../model/job-state";

export function JobStateBadge({ state }: { state: JobState }) {
	return (
		<Badge tone={jobStateTone(state)} dot>
			{jobStateLabel(state)}
		</Badge>
	);
}
