import { TrendLineChart } from "../src/dashboard/index.js";
import {
	MetricCard,
	StatTile,
	KPIRow,
	MetricAreaChart,
	MetricBarChart,
	MetricLineChart,
	type MetricState,
} from "../src/dashboard/index.js";

export default { title: "Shared UI/Metrics" };
const series = [
	{
		name: "Requests",
		data: [
			{ label: "Mon", value: 12 },
			{ label: "Tue", value: 24 },
			{ label: "Wed", value: 18 },
		],
	},
	{
		name: "Errors",
		data: [
			{ label: "Mon", value: 2 },
			{ label: "Wed", value: 1 },
		],
	},
];
export const Populated = {
	render: () => (
		<MetricCard
			metric={{
				label: "Requests",
				value: 54,
				delta: 0.12,
				series: [12, 24, 18],
				state: "ready",
				provenance: {
					source: "Example metric",
					observedAt: "2026-01-01T12:00:00Z",
					owner: "Acme",
				},
			}}
		/>
	),
};
export const Availability = {
	render: () => (
		<div>
			{(
				[
					"loading",
					"no_data",
					"partial",
					"stale",
					"provider_unavailable",
					"not_configured",
				] satisfies MetricState[]
			).map((state) => (
				<StatTile key={state} metric={{ label: state, value: 54, state }} />
			))}
		</div>
	),
};
export const Comparison = {
	render: () => (
		<KPIRow
			metrics={[
				{ label: "Revenue", value: 1234, format: "currency", delta: 0.15 },
				{ label: "Errors", value: 2, delta: -0.2, higherIsBetter: false },
				{ label: "Conversion", value: 45, format: "percent", delta: 0 },
			]}
		/>
	),
};
export const Lines = {
	render: () => <MetricLineChart title="Requests over time" series={series} />,
};
export const Areas = {
	render: () => <MetricAreaChart title="Request volume" series={series} />,
};
export const Bars = {
	render: () => <MetricBarChart title="Requests by day" series={series} />,
};
export const EmptyChart = {
	render: () => <MetricLineChart title="No requests" series={[]} />,
};

export const IndexedTrend = {
	render: () => <TrendLineChart points={[5, 10, 7]} />,
};
