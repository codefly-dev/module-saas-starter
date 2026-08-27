import {
	Dashboard,
	dashboard,
	event,
	Layout,
	metric,
} from "@/features/dashboard";

// Reference dashboard: declare events → metrics → a dashboard, then render it.
// Everything below the imports is the entire consumer surface.
const login = event("auth.login", "Logins");

const insights = dashboard({
	title: "Activity",
	description: "Live from your organization's audit trail.",
	metrics: [
		metric({
			title: "Logins over time",
			event: login,
			groupBy: "time",
			bucket: "day",
			chart: "line",
		}),
		metric({
			title: "Top event types",
			groupBy: "event_type",
			chart: "bar",
			limit: 6,
		}),
		metric({
			title: "Total logins",
			event: login,
			groupBy: "time",
			bucket: "day",
			chart: "stat",
		}),
	],
});

export default function InsightsPage() {
	return (
		<Layout title="Insights" description="A dashboard in a few lines.">
			<Dashboard data={insights} />
		</Layout>
	);
}
