import { useState } from "react";
import {
	Accordion,
	AccordionContent,
	AccordionHeader,
	AccordionItem,
	AccordionTrigger,
	Breadcrumb,
	Button,
	Checkbox,
	Chip,
	ChipGroup,
	DescriptionList,
	Disclosure,
	Input,
	Label,
	Popover,
	PopoverContent,
	PopoverTrigger,
	Radio,
	RadioGroup,
	Stack,
	Surface,
	Timeline,
	Tree,
	ViewportOverlay,
} from "../src/layout/index.js";

export default { title: "Kit/Navigation and selection" };
export const Disclosures = {
	render: () => (
		<Stack>
			<Disclosure title="Evidence" defaultOpen>
				Exact source content supplied by the caller.
			</Disclosure>
			<Accordion defaultValue={["first"]}>
				{["first", "second"].map((value) => (
					<AccordionItem key={value} value={value}>
						<AccordionHeader>
							<AccordionTrigger>{value}</AccordionTrigger>
						</AccordionHeader>
						<AccordionContent>Details for {value}</AccordionContent>
					</AccordionItem>
				))}
			</Accordion>
		</Stack>
	),
};
export const Breadcrumbs = {
	render: () => (
		<Breadcrumb
			items={[
				{ id: "root", label: "Library", href: "#library" },
				{ id: "group", label: "Group A", href: "#group" },
				{ id: "item", label: "Current item" },
			]}
		/>
	),
};
export const Radios = {
	render: () => (
		<RadioGroup defaultValue="useful" aria-label="Feedback">
			<Label>
				<Radio value="useful" />
				Useful
			</Label>
			<Label>
				<Radio value="incomplete" />
				Incomplete
			</Label>
			<Label>
				<Radio value="unavailable" disabled />
				Unavailable
			</Label>
		</RadioGroup>
	),
};
export const BareSurface = {
	render: () => (
		<Surface>
			<Stack direction="row" gap={2}>
				<span>A surface</span>
				<span>whose caller controls its layout.</span>
			</Stack>
		</Surface>
	),
};
function InteractiveTree() {
	const [selected, setSelected] = useState<string>();
	return (
		<Tree
			label="Collection"
			selectedId={selected}
			onSelect={(node) => setSelected(node.id)}
			defaultExpandedIds={["group"]}
			items={[
				{
					id: "group",
					label: "Group A",
					textValue: "Group A",
					children: [
						{ id: "first", label: "First item", textValue: "First item" },
						{ id: "second", label: "Second item", textValue: "Second item" },
					],
				},
				{
					id: "loading",
					label: "Reading children",
					textValue: "Reading children",
					hasChildren: true,
					loading: true,
				},
			]}
		/>
	);
}
export const Trees = { render: () => <InteractiveTree /> };
export const LargeTree = {
	render: () => (
		<Tree
			label="Large collection"
			virtualize={{ height: 240, rowHeight: 40 }}
			items={Array.from({ length: 1000 }, (_, index) => ({
				id: String(index),
				label: `Item ${index}`,
				textValue: `Item ${index}`,
			}))}
		/>
	),
};
export const Timelines = {
	render: () => (
		<Timeline
			label="Recorded sequence"
			entries={[
				{
					id: "a",
					title: "Started",
					time: { dateTime: "2026-01-01T12:00:00Z", label: "12:00" },
				},
				{
					id: "b",
					title: "Checked",
					description: "No timestamp was recorded for this entry.",
				},
				{
					id: "c",
					title: "Finished",
					time: { dateTime: "2026-01-01T12:01:00Z", label: "12:01" },
				},
			]}
		/>
	),
};
function ChoiceExample() {
	const [selected, setSelected] = useState<string[]>([]),
		[query, setQuery] = useState("");
	const options = ["Group A", "Group B", "Group C"];
	const toggle = (option: string) =>
		setSelected((current) =>
			current.includes(option)
				? current.filter((value) => value !== option)
				: [...current, option],
		);
	return (
		<Stack>
			<ChipGroup label="Chosen groups">
				{selected.map((option) => (
					<Chip
						key={option}
						onRemove={() => toggle(option)}
						removeLabel={`Remove ${option}`}
					>
						{option}
					</Chip>
				))}
			</ChipGroup>
			<Popover>
				<PopoverTrigger render={<Button variant="outline" />}>
					Choose groups
				</PopoverTrigger>
				<PopoverContent aria-label="Choose groups">
					<Stack>
						<Input
							aria-label="Find a group"
							value={query}
							onChange={(event) => setQuery(event.target.value)}
						/>
						{options
							.filter((option) =>
								option.toLowerCase().includes(query.toLowerCase()),
							)
							.map((option) => (
								<Label key={option}>
									<Checkbox
										checked={selected.includes(option)}
										onCheckedChange={() => toggle(option)}
									/>
									{option}
								</Label>
							))}
					</Stack>
				</PopoverContent>
			</Popover>
		</Stack>
	);
}
export const SearchableChoices = { render: () => <ChoiceExample /> };

export const LongDescriptions = {
	render: () => (
		<DescriptionList
			layout="stacked"
			items={[{ term: "Release", value: "a".repeat(160) }]}
		/>
	),
};
export const Overlays = {
	render: () => (
		<div style={{ position: "relative", height: 80 }}>
			<ViewportOverlay
				items={[
					{
						id: "example",
						stroke: "currentColor",
						fill: "transparent",
						regions: [
							{ kind: "rect", x: 8, y: 8, width: 120, height: 40 },
							{
								kind: "polygon",
								points: [
									[160, 8],
									[200, 48],
									[160, 48],
								],
							},
						],
					},
				]}
			/>
		</div>
	),
};
