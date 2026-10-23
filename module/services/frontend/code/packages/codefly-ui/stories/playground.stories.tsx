import { type ReactNode, useState } from "react";
import { Button } from "../src/layout/index.js";
import {
	CapabilityGuide,
	OperationLog,
	PlaygroundShell,
	ScenarioSteps,
	Step,
	type PlaygroundOperation,
} from "../src/playground/index.js";

const meta = { title: "Shared UI/Playground" };
export default meta;

const scenarios = [
	{
		id: "inspect",
		label: "Inspect a response",
		description:
			"Follow a fixture request from the public operation to its response.",
	},
	{
		id: "refusal",
		label: "Understand a refusal",
		description: "A refused operation keeps its reason beside the request.",
	},
];
const calls: PlaygroundOperation[] = [
	{
		id: "call-1",
		operation: "Example.Read",
		request: { id: "example-1" },
		response: { id: "example-1", name: "Example item" },
		state: "done",
		outcome: "The item was returned.",
	},
	{
		id: "call-2",
		operation: "Example.Update",
		request: { id: "example-1" },
		response: { code: "permission_denied" },
		state: "failed",
		refusal: "This fixture principal has read access only.",
	},
];

function Fixture({ actions }: { actions?: ReactNode }) {
	const [active, setActive] = useState("inspect");
	const [entries, setEntries] = useState<PlaygroundOperation[]>([]);
	const call = calls[active === "inspect" ? 0 : 1];
	return (
		<PlaygroundShell
			title="Example playground"
			moduleName="Example Module"
			composition="Example Composition"
			scenarios={scenarios}
			active={active}
			onSelect={setActive}
			actions={actions}
		>
			<CapabilityGuide
				capabilities={[
					{
						id: "read",
						title: "Read an item",
						description: "Inspect a structured response.",
						status: "demonstrated",
					},
					{
						id: "update",
						title: "Update an item",
						description: "Understand an authorization refusal.",
						status: "demonstrated",
					},
					{
						id: "list",
						title: "List items",
						description: "Browse a collection.",
						status: "reachable",
						reason:
							"Available through the public API; this fixture does not call it.",
					},
					{
						id: "export",
						title: "Export items",
						description: "Produce a portable copy.",
						status: "unreachable",
						reason: "No export handler is connected in this composition.",
					},
				]}
			/>
			<ScenarioSteps>
				<Step
					title={
						active === "inspect" ? "Inspect the item" : "Try a protected change"
					}
					{...call}
				>
					<Button
						onClick={() =>
							setEntries((all) => [
								...all,
								{ ...call, id: `call-${all.length + 1}` },
							])
						}
					>
						Record fixture call
					</Button>
				</Step>
				<Step
					title="Inspect the next response"
					operation="Example.Check"
					request={{ id: "example-1" }}
					state="running"
				/>
				<Step
					title="Continue the walk"
					operation="Example.Finish"
					request={{ id: "example-1" }}
					state="pending"
				/>
			</ScenarioSteps>
			<OperationLog entries={entries} />
		</PlaygroundShell>
	);
}

export const FixtureScenario = {
	render: (props: { actions?: ReactNode } = {}) => <Fixture {...props} />,
};
