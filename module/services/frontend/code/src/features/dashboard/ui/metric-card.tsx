import { AlertTriangle, BarChart3 } from "lucide-react";
import { Sparkline } from "@/components/sparkline";
import { formatAuditAction } from "@/features/audit/model/transforms";
import {
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	Skeleton,
} from "@/shared/ui";
import type { MetricDef } from "../model/schema";
import { useMetric } from "../service/use-metric";
import { BarList } from "./charts/bar-list";
import { LineChart } from "./charts/line-chart";

// MetricCard renders a single declared metric: it resolves the data graph node
// via useMetric and paints the chart the metric asked for. Loading and empty
// are handled here so a declaration never has to.
export function MetricCard({
	metric,
	orgId,
	className,
}: {
	metric: MetricDef;
	orgId: string;
	className?: string;
}) {
	const { points, total, status } = useMetric(metric, orgId);

	return (
		<Card className={className}>
			<CardHeader className="pb-2">
				<CardTitle className="text-base">{metric.title}</CardTitle>
				{metric.description && (
					<CardDescription>{metric.description}</CardDescription>
				)}
			</CardHeader>
			<CardContent>
				{status === "loading" ? (
					<Skeleton className="h-[120px] w-full" />
				) : status === "error" ? (
					<div className="flex items-center gap-2 py-6 text-sm text-destructive">
						<AlertTriangle className="h-4 w-4" />
						Unable to load this metric.
					</div>
				) : points.length === 0 ? (
					<div className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
						<BarChart3 className="h-4 w-4" />
						No events yet.
					</div>
				) : metric.chart === "stat" ? (
					<div className="flex items-end justify-between gap-4">
						<span className="text-4xl font-bold tabular-nums tracking-tight">
							{total.toLocaleString()}
						</span>
						<Sparkline
							points={points.map((p) => p.value)}
							width={120}
							height={40}
							className="text-primary/70"
						/>
					</div>
				) : metric.chart === "line" ? (
					<LineChart
						points={points.map((p) => p.value)}
						className="text-primary/70"
					/>
				) : (
					<BarList
						items={points.map((p) => ({
							label: formatAuditAction(p.key),
							value: p.value,
						}))}
					/>
				)}
			</CardContent>
		</Card>
	);
}
