"use client";

import { RefreshCw } from "lucide-react";
import { readOutcome } from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import {
	Banner,
	Button,
	KPIRow,
	type Metric,
	Page as PageBody,
	PageHeader,
	Section,
	useLoadingPhase,
} from "@/shared/ui";
import { backlogNotice, relayTiles } from "../model/event-labels";
import { useEventOperations, useEventSubscriptions } from "../service/queries";
import { EventTypesTable } from "./event-types-table";
import { SubscriptionsTable } from "./subscriptions-table";
import { UndeliveredTable } from "./undelivered-table";

export function EventOperationsPage() {
	const operations = useEventOperations();
	const subscriptions = useEventSubscriptions();

	// A refused read takes what an earlier read left on screen with it.
	const operationsRefused =
		readOutcome(operations.isError, operations.error) === "forbidden";
	const answer = operationsRefused ? undefined : operations.data;
	const subscriptionsRefused =
		readOutcome(subscriptions.isError, subscriptions.error) === "forbidden";

	const { indicator } = useLoadingPhase(operations.isLoading);
	const metrics: Metric[] = relayTiles(answer?.relay).map((tile) => ({
		...tile,
		...(indicator
			? { state: "loading" as const }
			: operationsRefused
				? { state: "forbidden" as const }
				: {}),
	}));
	const behind = answer?.relay
		? backlogNotice(
				Number(answer.relay.backlog),
				answer.relay.oldestUnpublishedAt
					? formatDate(answer.relay.oldestUnpublishedAt)
					: null,
			)
		: null;
	const refreshing = operations.isFetching || subscriptions.isFetching;

	return (
		<PageBody>
			<PageHeader
				title="Event operations"
				description="The events the platform publishes, who subscribes to them, how far delivery is behind, and what could not be delivered. Event contents are never shown here."
				actions={
					<Button
						variant="outline"
						onClick={() =>
							Promise.all([operations.refetch(), subscriptions.refetch()])
						}
						disabled={refreshing}
					>
						<RefreshCw />
						Refresh
					</Button>
				}
			/>

			<KPIRow metrics={metrics} className="lg:grid-cols-3" />
			{behind && <Banner tone="warning">{behind}</Banner>}

			<Section
				title="Event types"
				description="Every declared event type with its counts. Types that were published without being declared are listed too."
			>
				<EventTypesTable
					eventTypes={answer?.eventTypes}
					isLoading={operations.isLoading}
					isError={operations.isError}
					error={operations.error}
					onRetry={() => void operations.refetch()}
				/>
			</Section>

			<Section
				title="Subscriptions"
				description="Every active subscription on the platform, with the number of events its queue could not deliver."
			>
				<SubscriptionsTable
					subscriptions={
						subscriptionsRefused ? undefined : subscriptions.data?.subscriptions
					}
					isLoading={subscriptions.isLoading}
					isError={subscriptions.isError}
					error={subscriptions.error}
					onRetry={() => void subscriptions.refetch()}
				/>
			</Section>

			{answer && answer.deadLetters.length > 0 && (
				<Section
					title="Undelivered events by queue"
					description="Events set aside after every delivery attempt failed."
				>
					<UndeliveredTable queues={answer.deadLetters} />
				</Section>
			)}
		</PageBody>
	);
}
