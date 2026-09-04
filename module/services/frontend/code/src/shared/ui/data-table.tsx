// Re-export of the kit's DataTable. It now ships once from @codefly-dev/ui/table
// as its single sealed home (issue #451); this module keeps the
// `@/shared/ui/data-table` import path stable for existing feature callers.
export { DataTable, type DataTableProps } from "@codefly-dev/ui/table";
