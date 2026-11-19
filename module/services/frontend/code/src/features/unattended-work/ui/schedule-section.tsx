"use client";

import { DescriptionList } from "@codefly-dev/ui/layout";
import { CalendarClock } from "lucide-react";
import Link from "next/link";
import type { SourceTriggerDeclaration } from "@/gen/saas/accounts/v1/datasource_pb";
import { Button, DelayedLoading, EmptyState, Section } from "@/shared/ui";
import {
	missedRunWords,
	momentWords,
	overlapWords,
	runsWithWords,
	scheduleWords,
	timeLimitWords,
	workWords,
} from "../model/words";

/**
 * What runs for one source with nobody signed in, and when. A source with no
 * schedule says so and points at where one is set, rather than showing a list
 * of blanks.
 */
export function ScheduleSection({
	declaration,
	isLoading,
}: {
	declaration: SourceTriggerDeclaration | undefined;
	isLoading: boolean;
}) {
	const schedule = declaration?.schedule;
	return (
		<Section
			title="Schedule"
			description="What runs for this source with nobody signed in, and when. The schedule follows the refresh interval in the source's settings."
		>
			{isLoading ? (
				<DelayedLoading active label="Loading the schedule" />
			) : !schedule || declaration?.undeclarableReason ? (
				<EmptyState
					icon={<CalendarClock />}
					heading="This source has no schedule"
					description="Nothing runs for it unattended. Give it a refresh interval in its settings and its schedule appears here."
				>
					<Button
						variant="outline"
						nativeButton={false}
						render={<Link href="/admin/datasources" />}
					>
						Open data sources
					</Button>
				</EmptyState>
			) : (
				<DescriptionList
					items={[
						{ term: "What runs", value: workWords(declaration?.operation) },
						{ term: "How often", value: scheduleWords(schedule.cron) },
						{ term: "Time zone", value: schedule.timezone || "Not specified" },
						{
							term: "Next run",
							value: momentWords(
								declaration?.nextOccurrenceAt,
								"Not yet known",
							),
						},
						{
							term: "Last run",
							value: momentWords(declaration?.lastOccurrenceAt, "Not yet run"),
						},
						{
							term: "If a run is missed",
							value: missedRunWords(schedule.missedRun),
						},
						{
							term: "If the previous run is still going",
							value: overlapWords(schedule.overlap),
						},
						{
							term: "Time limit for one run",
							value: timeLimitWords(schedule.deadline),
						},
						{
							term: "Runs with",
							value: runsWithWords(declaration?.identity),
						},
					]}
				/>
			)}
		</Section>
	);
}
