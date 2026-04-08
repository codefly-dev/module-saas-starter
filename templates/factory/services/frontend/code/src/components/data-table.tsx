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
      <div className="rounded-lg border border-gray-200 dark:border-gray-800">
        <div className="p-8 text-center text-gray-500">Loading...</div>
      </div>
    );
  }

  if (data.length === 0) {
    return (
      <div className="rounded-lg border border-gray-200 dark:border-gray-800">
        <div className="p-8 text-center text-gray-500">{emptyMessage}</div>
      </div>
    );
  }

  return (
    <div className="rounded-lg border border-gray-200 dark:border-gray-800 overflow-hidden">
      <table className="w-full text-sm">
        <thead className="bg-gray-50 dark:bg-gray-900 border-b border-gray-200 dark:border-gray-800">
          <tr>
            {columns.map((col) => (
              <th
                key={String(col.key)}
                className="px-4 py-3 text-left font-medium text-gray-500 dark:text-gray-400"
              >
                {col.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-200 dark:divide-gray-800">
          {data.map((row) => (
            <tr key={getRowKey(row)} className="hover:bg-gray-50 dark:hover:bg-gray-900/50">
              {columns.map((col) => {
                const value = (row as Record<string, unknown>)[String(col.key)];
                return (
                  <td key={String(col.key)} className={`px-4 py-3 ${col.className ?? ""}`}>
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
