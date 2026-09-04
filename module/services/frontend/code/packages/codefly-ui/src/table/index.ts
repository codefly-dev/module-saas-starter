// The table composite tier: data-in tables built on the layout `Table` atoms
// plus pagination / skeleton / empty states. Exported from `@codefly-dev/ui/table`.
// Composes the layout tier below it — never a sibling composite. React only.

export {
	DataTable,
	type DataTableColumn,
	type SortDirection,
	type SortState,
} from "./data-table.js";
