"use client";

export interface Column<T> {
	key: keyof T | string;
	label: string;
	sortable?: boolean;
	render?: (value: unknown, row: T) => React.ReactNode;
	className?: string;
}

interface DataTableProps<T> {
	columns: Column<T>[];
	data: T[];
	isLoading: boolean;
	emptyMessage?: string;
	getRowKey: (row: T) => string;
}

export function DataTable<T>({
	columns,
	data,
	isLoading,
	emptyMessage = "No data found",
	getRowKey,
}: DataTableProps<T>) {
	if (isLoading) {
		return (
			<div className="rounded-lg border">
				<div className="p-8 text-center text-muted-foreground">Loading...</div>
			</div>
		);
	}

	if (data.length === 0) {
		return (
			<div className="rounded-lg border">
				<div className="p-8 text-center text-muted-foreground">
					{emptyMessage}
				</div>
			</div>
		);
	}

	return (
		<div className="overflow-hidden rounded-lg border">
			<table className="w-full text-sm">
				<thead className="border-b bg-muted/50">
					<tr>
						{columns.map((col) => (
							<th
								key={String(col.key)}
								className="px-4 py-3 text-left font-medium text-muted-foreground"
							>
								{col.label}
							</th>
						))}
					</tr>
				</thead>
				<tbody className="divide-y divide-border">
					{data.map((row) => (
						<tr key={getRowKey(row)} className="hover:bg-muted/50">
							{columns.map((col) => {
								const value = (row as Record<string, unknown>)[String(col.key)];
								return (
									<td
										key={String(col.key)}
										className={`px-4 py-3 ${col.className ?? ""}`}
									>
										{col.render ? col.render(value, row) : String(value ?? "-")}
									</td>
								);
							})}
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}
