"use client";

import { Button, ErrorState, Panel, Stack } from "@/shared/ui";
import { readFailureWords } from "../model/read-failure";

/**
 * A read that did not come back, in place of the list it was for. A refusal
 * says who can grant the role; only a real failure offers another try.
 */
export function ReadFailure({
	subject,
	error,
	onRetry,
}: {
	/** What was being read, as the page calls it: "the queues". */
	subject: string;
	error: unknown;
	onRetry: () => void;
}) {
	const words = readFailureWords(subject, error);
	return (
		<Panel>
			<Stack
				direction="row"
				gap={4}
				align="center"
				justify="between"
				className="flex-wrap"
			>
				<ErrorState title={words.title} detail={words.detail} />
				{words.canRetry && (
					<Button variant="outline" onClick={onRetry}>
						Retry
					</Button>
				)}
			</Stack>
		</Panel>
	);
}
