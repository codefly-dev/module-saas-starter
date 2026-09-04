import { ArrowDownIcon, ArrowUpIcon } from "lucide-react";
import type * as React from "react";
import { Card } from "./card.js";
import { cn } from "./cn.js";

type DeltaTone = "positive" | "negative" | "neutral";

/** A single KPI: a label, a large value, an optional period-over-period delta,
 * and an optional trailing slot for an icon or sparkline. Composes `Card` for
 * its surface so it themes with every other card. */
function StatTile({
	label,
	value,
	delta,
	deltaLabel,
	deltaTone,
	icon,
	visual,
	className,
	...props
}: Omit<React.ComponentProps<"div">, "children"> & {
	label: React.ReactNode;
	value: React.ReactNode;
	/** e.g. "+12%". Sign drives the arrow and default color. */
	delta?: string;
	/** Caption after the delta, e.g. "vs last week". */
	deltaLabel?: React.ReactNode;
	/** Override the color; by default a leading "-" reads negative. */
	deltaTone?: DeltaTone;
	icon?: React.ReactNode;
	/** A trailing visual — a sparkline (`StatChart`), a small chart, etc. */
	visual?: React.ReactNode;
}) {
	const isNegativeValue = delta?.trim().startsWith("-") ?? false;
	// Color reads the semantics (a caller may mark a decline "positive", e.g.
	// fewer failed logins); the arrow always follows the number's actual sign.
	const tone: DeltaTone =
		deltaTone ??
		(isNegativeValue ? "negative" : delta ? "positive" : "neutral");
	return (
		<Card className={cn("flex flex-col gap-2", className)} {...props}>
			<div className="flex items-start justify-between gap-2">
				<span className="text-sm font-medium text-muted-foreground">
					{label}
				</span>
				{icon && (
					<span className="text-muted-foreground [&_svg]:size-4">{icon}</span>
				)}
			</div>
			<div className="flex items-end justify-between gap-3">
				<span className="font-heading text-3xl font-semibold tracking-tight tabular-nums">
					{value}
				</span>
				{visual && <div className="min-w-0 flex-1 pb-1">{visual}</div>}
			</div>
			{delta && (
				<div className="flex items-center gap-1.5 text-xs">
					<span
						className={cn(
							"inline-flex items-center gap-0.5 rounded-full px-1.5 py-0.5 font-medium tabular-nums",
							tone === "positive" &&
								"bg-[color-mix(in_oklch,var(--chart-2)_15%,transparent)] text-[var(--chart-2)]",
							tone === "negative" && "bg-destructive/10 text-destructive",
							tone === "neutral" && "bg-muted text-muted-foreground",
						)}
					>
						{isNegativeValue ? (
							<ArrowDownIcon className="size-3" aria-hidden />
						) : (
							<ArrowUpIcon className="size-3" aria-hidden />
						)}
						{delta}
					</span>
					{deltaLabel && (
						<span className="text-muted-foreground">{deltaLabel}</span>
					)}
				</div>
			)}
		</Card>
	);
}

export { StatTile };
