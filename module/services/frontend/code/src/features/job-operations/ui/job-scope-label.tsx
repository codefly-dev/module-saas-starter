"use client";

import { ResourceLabel } from "@/components/resource-label";
import type { JobSummary } from "@/gen/saas/jobs/v1/jobs_pb";

/** Who a job is for: one organization, one person, or the whole platform. */
export function JobScopeLabel({ job }: { job: JobSummary }) {
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
			return <span className="text-muted-foreground">Not recorded</span>;
	}
}
