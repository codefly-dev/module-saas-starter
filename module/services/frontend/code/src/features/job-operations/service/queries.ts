import { useQuery } from "@tanstack/react-query";
import type { JobState } from "@/gen/saas/jobs/v1/jobs_pb";
import { usePlatformAdminService } from "@/lib/hooks/use-api-client";

export interface JobListFilters {
	queue: string;
	state?: JobState;
	pageToken: string;
}

export function useJobOperations(queue: string) {
	const service = usePlatformAdminService();
	return useQuery({
		queryKey: ["job-operations", queue],
		queryFn: () => service.getJobOperations({ queue }),
		refetchInterval: 15_000,
	});
}

export function useJobs(filters: JobListFilters) {
	const service = usePlatformAdminService();
	return useQuery({
		queryKey: ["jobs", filters],
		queryFn: () =>
			service.listJobs({
				queue: filters.queue,
				states: filters.state === undefined ? [] : [filters.state],
				scope: { case: undefined },
				pageSize: 25,
				pageToken: filters.pageToken,
			}),
	});
}

export function useJob(jobId: string | null) {
	const service = usePlatformAdminService();
	return useQuery({
		queryKey: ["job", jobId],
		queryFn: () => service.getJob({ jobId: jobId! }),
		enabled: jobId !== null,
	});
}
