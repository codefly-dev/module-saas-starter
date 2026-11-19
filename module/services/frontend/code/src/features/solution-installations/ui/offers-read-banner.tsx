import { readOutcome } from "@/shared/lib/read-outcome";
import { Banner, Button } from "@/shared/ui";
import { hostRefusal } from "../model/installations";

/**
 * Said when the authority's offers could not be read: nothing is classified
 * without them, so the lists below are short for that reason and not because
 * nothing is offered. A refusal names who can grant the read; only a read
 * that did not complete offers a retry.
 */
export function OffersReadBanner({
	error,
	showModules,
	retrying,
	onRetry,
}: {
	error: unknown;
	showModules: boolean;
	retrying: boolean;
	onRetry: () => void;
}) {
	const forbidden = readOutcome(true, error) === "forbidden";
	return (
		<Banner
			tone="danger"
			title="Couldn't read what this organization can install"
		>
			{hostRefusal(error)}{" "}
			{showModules
				? "A module is listed here only once that is read, so no module is listed yet."
				: "A solution is listed once it is offered to this organization, so no solution is listed yet."}{" "}
			{forbidden ? (
				"Ask an organization administrator to give you access."
			) : (
				<Button
					size="sm"
					variant="outline"
					disabled={retrying}
					onClick={onRetry}
				>
					{retrying ? "Retrying…" : "Retry"}
				</Button>
			)}
		</Banner>
	);
}
