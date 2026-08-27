// BarList — a ranked horizontal bar chart for a categorical metric. The same
// row shape the admin audit page draws inline (label, count, proportional
// track), lifted into a reusable primitive so a declared metric renders it
// without hand-wiring.
interface BarListItem {
	label: string;
	value: number;
}

export function BarList({ items }: { items: BarListItem[] }) {
	const max = items.reduce((m, i) => Math.max(m, i.value), 0) || 1;
	return (
		<div className="space-y-2">
			{items.map((item) => (
				<div key={item.label} className="space-y-1">
					<div className="flex items-center justify-between text-xs">
						<span className="text-muted-foreground">{item.label}</span>
						<span className="font-mono">{item.value}</span>
					</div>
					<div className="h-2 rounded-full bg-muted">
						<div
							className="h-2 rounded-full bg-primary/70"
							style={{ width: `${(item.value / max) * 100}%` }}
						/>
					</div>
				</div>
			))}
		</div>
	);
}
