import { readOutcome } from "@/shared/lib/read-outcome";

export interface ReadFailureWords {
	title: string;
	detail: string;
	/** A refusal is not something trying again changes. */
	canRetry: boolean;
}

/**
 * What to say when a read on this page did not come back. `subject` is what
 * was being read, as the page calls it ("the queues", "this job").
 */
export function readFailureWords(
	subject: string,
	error: unknown,
): ReadFailureWords {
	if (readOutcome(true, error) === "forbidden") {
		return {
			title: `You can't see ${subject}`,
			detail:
				"Your account doesn't have the super administrator role this page needs. Ask a platform super administrator to grant it.",
			canRetry: false,
		};
	}
	return {
		title: `Couldn't load ${subject}`,
		detail:
			"Something went wrong on the way, so what is there is unknown. If it keeps failing, tell your administrator.",
		canRetry: true,
	};
}
