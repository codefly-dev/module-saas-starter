import {
	type ReadOutcome,
	readOutcomeMessage,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
import { Button, ErrorState } from "@/shared/ui";
import { hostRefusal } from "../model/installations";

/**
 * A list read that ended with nothing to show because it was refused or did
 * not complete: which of the two, the host's own words when it refused, who
 * can grant what is missing, and a retry only where retrying can help.
 */
export function ListReadFailure({
	outcome,
	subject,
	error,
	retrying,
	onRetry,
}: {
	outcome: Exclude<ReadOutcome, "empty">;
	/** What the list holds, as the page calls it. */
	subject: string;
	error: unknown;
	retrying: boolean;
	onRetry: () => void;
}) {
	return (
		<div className="space-y-3 py-4">
			<ErrorState
				title={readOutcomeMessage(outcome, subject, "")}
				detail={
					outcome === "forbidden"
						? `${hostRefusal(error)} Ask an organization administrator to give you access.`
						: hostRefusal(error)
				}
			/>
			{outcome === "failed" && (
				<Button
					variant="outline"
					size="sm"
					disabled={retrying}
					onClick={onRetry}
				>
					{retrying ? "Retrying…" : "Retry"}
				</Button>
			)}
		</div>
	);
}

/** Said above rows that survived a refresh which did not complete. */
export function StaleRowsNotice({
	outcome,
	subject,
	error,
}: {
	outcome: ReadOutcome;
	subject: string;
	error: unknown;
}) {
	const stale = staleReadNotice(outcome, subject);
	if (!stale) return null;
	return (
		<p role="status" className="text-sm text-muted-foreground">
			{stale} {hostRefusal(error)}
		</p>
	);
}
