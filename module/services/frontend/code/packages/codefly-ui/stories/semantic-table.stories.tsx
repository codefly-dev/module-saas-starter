import { useState } from "react";
import {
	Checkbox,
	Table,
	TableBody,
	TableCaption,
	TableCell,
	TableFooter,
	TableHead,
	TableHeader,
	TableRow,
} from "../src/layout/index.js";

export default { title: "Shared UI/Semantic table" };

const workspaces = [
	{ name: "Acme", members: 3 },
	{ name: "ExampleCorp", members: 7 },
];

export const SelectableSummary = {
	render: function SelectableSummaryStory() {
		const [selected, setSelected] = useState<string[]>(["Acme"]);
		return (
			<Table>
				<TableCaption>
					Workspace membership — {selected.length} selected
				</TableCaption>
				<TableHeader>
					<TableRow>
						<TableHead scope="col">Select</TableHead>
						<TableHead scope="col">Workspace</TableHead>
						<TableHead scope="col">Members</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{workspaces.map(({ name, members }) => (
						<TableRow
							key={name}
							data-state={selected.includes(name) ? "selected" : undefined}
						>
							<TableCell>
								<Checkbox
									aria-label={`Select ${name}`}
									checked={selected.includes(name)}
									onCheckedChange={(checked) =>
										setSelected((current) =>
											checked
												? [...current, name]
												: current.filter((value) => value !== name),
										)
									}
								/>
							</TableCell>
							<TableHead scope="row">{name}</TableHead>
							<TableCell>{members}</TableCell>
						</TableRow>
					))}
				</TableBody>
				<TableFooter>
					<TableRow>
						<TableHead scope="row" colSpan={2}>
							Total members
						</TableHead>
						<TableCell>10</TableCell>
					</TableRow>
				</TableFooter>
			</Table>
		);
	},
};

export const EmptySummary = {
	render: () => (
		<Table>
			<TableCaption>Workspace membership</TableCaption>
			<TableHeader>
				<TableRow>
					<TableHead scope="col">Workspace</TableHead>
					<TableHead scope="col">Members</TableHead>
				</TableRow>
			</TableHeader>
			<TableBody>
				<TableRow>
					<TableCell colSpan={2}>No workspaces found.</TableCell>
				</TableRow>
			</TableBody>
		</Table>
	),
};
