import {
	Dashboard,
	dashboard,
	event,
	Layout,
	metric,
} from "@/features/dashboard";

// A second reference dashboard, alongside /insights. It exercises dimensions
// the first one doesn't — grouping by category and scoping a metric to one
// category — to show the data graph generalizes past a single page. Everything
// below the imports is the entire consumer surface.
const mfaVerified = event("mfa.totp_verified");

const security = dashboard({
	title: "Security",
	description: "Live from your organization's security-relevant audit trail.",
	metrics: [
		metric({
			title: "Events by category",
			groupBy: "category",
			chart: "bar",
		}),
		metric({
			title: "Security events over time",
			category: "security",
			groupBy: "time",
			bucket: "week",
			chart: "line",
		}),
		metric({
			title: "MFA verifications",
			event: mfaVerified,
			groupBy: "time",
			bucket: "day",
			chart: "stat",
		}),
	],
});

export default function SecurityPage() {
	return (
		<Layout
			title="Security"
			description="A second dashboard, still a few lines."
		>
			<Dashboard data={security} />
		</Layout>
	);
}
