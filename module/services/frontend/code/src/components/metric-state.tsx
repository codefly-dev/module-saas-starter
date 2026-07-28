import { Badge } from "@/components/ui/badge";

export type MetricState =
	| "loading"
	| "no_data"
	| "partial"
	| "stale"
	| "provider_unavailable"
	| "not_configured"
	| "sample"
	| "ready";

const stateLabels: Record<Exclude<MetricState, "ready">, string> = {
	loading: "Loading",
	no_data: "No data",
	partial: "Partial data",
	stale: "Stale",
	provider_unavailable: "Provider unavailable",
	not_configured: "Not configured",
	sample: "Sample data",
};

export function assertSampleModeAllowed(
	sample: boolean,
	environment: string | undefined = process.env.NODE_ENV,
) {
	if (sample && environment === "production") {
		throw new Error("Sample metric mode is disabled in production");
	}
}

export function MetricStateBadge({ state }: { state: MetricState }) {
	if (state === "ready") return null;
	assertSampleModeAllowed(state === "sample");
	return (
		<Badge
			variant={state === "provider_unavailable" ? "destructive" : "outline"}
		>
			{stateLabels[state]}
		</Badge>
	);
}

export function MetricProvenance({
	source,
	observedAt,
	owner,
	timezone = "UTC",
}: {
	source: string;
	observedAt?: string;
	owner?: string;
	timezone?: string;
}) {
	return (
		<div className="flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
			<span>Source: {source}</span>
			<span>Timezone: {timezone}</span>
			<span>
				Freshness:{" "}
				{observedAt ? new Date(observedAt).toLocaleString() : "unknown"}
			</span>
			{owner && <span>Owner: {owner}</span>}
		</div>
	);
}
