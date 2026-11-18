"use client";

import { useState } from "react";
import type { JobState } from "@/gen/saas/jobs/v1/jobs_pb";
import {
	Button,
	Field,
	Input,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Stack,
} from "@/shared/ui";
import { JOB_STATE_FILTERS, jobStateLabel } from "../model/job-state";

const ALL_STATES = "all";

const stateItems = [
	{ value: ALL_STATES, label: "All states" },
	...JOB_STATE_FILTERS.map((state) => ({
		value: String(state),
		label: jobStateLabel(state),
	})),
];

/**
 * The two ways to narrow the jobs list. The queue name applies on Enter or
 * "Apply filter", so a half-typed name does not issue a read per keystroke;
 * the state applies as soon as it is chosen.
 */
export function JobsFilters({
	queue,
	state,
	onQueueChange,
	onStateChange,
	onClear,
}: {
	/** The applied queue filter, "" for every queue. */
	queue: string;
	state: JobState | undefined;
	onQueueChange: (queue: string) => void;
	onStateChange: (state: JobState | undefined) => void;
	onClear: () => void;
}) {
	// The draft follows the applied value when it changes from outside (Clear).
	const [draft, setDraft] = useState(queue);
	const [applied, setApplied] = useState(queue);
	if (applied !== queue) {
		setApplied(queue);
		setDraft(queue);
	}
	const apply = () => onQueueChange(draft.trim());
	const filtered = queue !== "" || state !== undefined;

	return (
		<Stack direction="row" gap={3} align="end" className="flex-wrap">
			<Field label="Queue" className="w-full sm:w-64">
				{(control) => (
					<Input
						{...control}
						value={draft}
						onChange={(event) => setDraft(event.target.value)}
						onKeyDown={(event) => event.key === "Enter" && apply()}
						placeholder="Exact queue name"
					/>
				)}
			</Field>
			<Field label="State" className="w-full sm:w-48">
				{(control) => (
					<Select
						value={state === undefined ? ALL_STATES : String(state)}
						items={stateItems}
						onValueChange={(value) =>
							onStateChange(
								value === ALL_STATES || value === null
									? undefined
									: (Number(value) as JobState),
							)
						}
					>
						<SelectTrigger id={control.id} className="w-full">
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							{stateItems.map((item) => (
								<SelectItem key={item.value} value={item.value}>
									{item.label}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				)}
			</Field>
			<Button variant="outline" onClick={apply}>
				Apply filter
			</Button>
			{filtered && (
				<Button variant="ghost" onClick={onClear}>
					Clear filters
				</Button>
			)}
		</Stack>
	);
}
