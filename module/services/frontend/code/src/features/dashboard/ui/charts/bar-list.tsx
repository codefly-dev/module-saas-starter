import { BarList as KitBarList } from "@codefly-dev/ui/dashboard";

export function BarList({
	items,
}: {
	items: { label: string; value: number }[];
}) {
	return (
		<KitBarList
			points={items.map(({ label, value }) => ({ key: label, value }))}
		/>
	);
}
