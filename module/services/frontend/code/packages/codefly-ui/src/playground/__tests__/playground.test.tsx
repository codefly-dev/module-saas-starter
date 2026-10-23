// @vitest-environment happy-dom
import {
	cleanup,
	fireEvent,
	render,
	screen,
	within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { renderToString } from "react-dom/server";
import { FixtureScenario } from "../../../stories/playground.stories.js";
import {
	CapabilityGuide,
	PlaygroundShell,
	PlaygroundThemeToggle,
	OperationLog,
	ScenarioSteps,
	Step,
} from "../index.js";

afterEach(() => {
	cleanup();
	document.documentElement.classList.remove("dark");
});

it("keeps every capability and gap reason visible, and navigates to a refusal", () => {
	render(<FixtureScenario.render />);
	expect(screen.getByText("Reachable, not demonstrated")).toBeTruthy();
	expect(
		screen.getByText("No export handler is connected in this composition."),
	).toBeTruthy();
	expect(screen.getByText("Running in Example Composition")).toBeTruthy();
	const next = screen.getByRole("button", { name: "Understand a refusal" });
	fireEvent.click(next);
	expect(next.getAttribute("aria-current")).toBe("page");
	expect(
		screen.getByText("This fixture principal has read access only."),
	).toBeTruthy();
	expect(screen.getByText("Pending")).toBeTruthy();
	expect(screen.getByText("Running")).toBeTruthy();
});

it("retains repeated calls in invocation order and expands their own exchanges", () => {
	render(<FixtureScenario.render />);
	const record = screen.getByRole("button", { name: "Record fixture call" });
	fireEvent.click(record);
	fireEvent.click(screen.getByRole("button", { name: "Understand a refusal" }));
	fireEvent.click(record);
	fireEvent.click(record);
	const log = screen
		.getByRole("heading", { name: "Operation log" })
		.closest("section")!;
	const entries = within(log).getAllByRole("listitem");
	expect(entries).toHaveLength(3);
	expect(entries.map((item) => item.textContent)).toEqual([
		"Example.ReadDone",
		"Example.UpdateFailed",
		"Example.UpdateFailed",
	]);
	fireEvent.click(within(entries[1]).getByRole("button"));
	expect(
		within(entries[1]).getByLabelText("Example.Update request"),
	).toBeTruthy();
	expect(
		within(entries[1]).getByLabelText("Example.Update response"),
	).toBeTruthy();
	expect(
		within(entries[1]).getByText(
			"This fixture principal has read access only.",
		),
	).toBeTruthy();
});

it("distinguishes no response from an explicit null response", () => {
	render(
		<ScenarioSteps>
			<Step
				title="Waiting"
				operation="Example.Wait"
				request={{}}
				state="pending"
			/>
			<Step
				title="Finished"
				operation="Example.Finish"
				request={{}}
				response={null}
				state="done"
			/>
		</ScenarioSteps>,
	);
	expect(screen.getAllByText("No response yet.")).toHaveLength(1);
	expect(
		screen.getByLabelText("Example.Finish response").textContent,
	).toContain("null");
});

it("renders on the server and preserves an initialized dark palette on mount", () => {
	expect(renderToString(<FixtureScenario.render />)).toContain(
		"Example playground",
	);
	document.documentElement.classList.add("dark");
	render(<FixtureScenario.render />);
	expect(document.documentElement.classList.contains("dark")).toBe(true);
});

it("names an empty operation log", () => {
	render(<OperationLog entries={[]} />);
	expect(screen.getByText("No operations called yet.")).toBeTruthy();
});

it("leaves the theme control and document palette with the caller", () => {
	const toggle = vi.fn();
	document.documentElement.classList.add("dark");
	const { rerender } = render(
		<FixtureScenario.render
			actions={<button onClick={toggle}>Light mode</button>}
		/>,
	);
	expect(screen.getAllByRole("button", { name: /Light mode/ })).toHaveLength(1);
	expect(screen.queryByRole("button", { name: "Dark mode" })).toBeNull();
	fireEvent.click(screen.getByRole("button", { name: "Light mode" }));
	expect(toggle).toHaveBeenCalledOnce();
	expect(document.documentElement.classList.contains("dark")).toBe(true);
	rerender(<FixtureScenario.render />);
	expect(
		screen.queryByRole("button", { name: /(Light|Dark) mode/ }),
	).toBeNull();
});

it("renders a controlled theme button without owning state or document effects", () => {
	const onModeChange = vi.fn();
	document.documentElement.classList.add("dark");
	const { rerender } = render(
		<PlaygroundThemeToggle mode="dark" onModeChange={onModeChange} />,
	);
	fireEvent.click(screen.getByRole("button", { name: "Light mode" }));
	expect(onModeChange).toHaveBeenLastCalledWith("light");
	expect(screen.getByRole("button", { name: "Light mode" })).toBeTruthy();
	expect(document.documentElement.classList.contains("dark")).toBe(true);
	rerender(
		<PlaygroundThemeToggle
			mode="light"
			onModeChange={onModeChange}
			labels={{ darkMode: "Use night palette" }}
		/>,
	);
	fireEvent.click(screen.getByRole("button", { name: "Use night palette" }));
	expect(onModeChange).toHaveBeenLastCalledWith("dark");
});

it("offers the kit theme control in the shell and permits host gutters and empty navigation copy", () => {
	const onModeChange = vi.fn();
	const { container } = render(
		<PlaygroundShell
			title="Example"
			description="Choose a demonstration later."
			moduleName="Example Module"
			composition="Example Composition"
			scenarios={[]}
			active=""
			onSelect={() => {}}
			className="p-0"
			theme={{ mode: "light", onModeChange }}
			labels={{
				emptyScenarios: "Nothing to explore yet.",
				navigationTitle: "Examples",
				navigation: "Example navigation",
				composition: "Composition:",
			}}
		>
			<p>Consumer content</p>
		</PlaygroundShell>,
	);
	fireEvent.click(screen.getByRole("button", { name: "Dark mode" }));
	expect(onModeChange).toHaveBeenCalledWith("dark");
	expect(screen.getByText("Choose a demonstration later.")).toBeTruthy();
	expect(
		within(
			screen.getByRole("navigation", { name: "Example navigation" }),
		).getByText("Nothing to explore yet."),
	).toBeTruthy();
	expect(
		screen.getByText("Composition: Example Composition").closest("aside"),
	).toBeNull();
	expect(screen.queryByRole("contentinfo")).toBeNull();
	expect(
		container.querySelector('[data-slot="layout"]')?.classList.contains("p-0"),
	).toBe(true);
	expect(
		container.querySelector('[data-slot="layout"]')?.classList.contains("p-6"),
	).toBe(false);
});

it("provides configurable section headings and empty states, including no steps", () => {
	render(
		<>
			<CapabilityGuide
				capabilities={[]}
				title="Available actions"
				emptyState="No actions yet."
			/>
			<ScenarioSteps title="Walkthrough" emptyState="No steps yet.">
				{[null, false]}
			</ScenarioSteps>
			<OperationLog entries={[]} title="Calls" emptyState="No calls yet." />
		</>,
	);
	for (const name of ["Available actions", "Walkthrough", "Calls"])
		expect(screen.getByRole("heading", { name })).toBeTruthy();
	for (const text of ["No actions yet.", "No steps yet.", "No calls yet."])
		expect(screen.getByText(text)).toBeTruthy();
	expect(screen.queryByRole("list")).toBeNull();
});

it("shows one status per log entry and applies caller copy to its exchange", () => {
	render(
		<OperationLog
			entries={[
				{
					id: "1",
					operation: "Example.Read",
					request: {},
					state: "failed",
					refusal: "Access refused.",
				},
			]}
			labels={{
				failed: "Refused",
				request: "Input",
				response: "Output",
				noResponse: "Still waiting.",
			}}
		/>,
	);
	fireEvent.click(
		screen.getByRole("button", { name: /Example.Read.*Refused/ }),
	);
	expect(screen.getAllByText("Refused")).toHaveLength(1);
	for (const text of ["Access refused.", "Input", "Output", "Still waiting."])
		expect(screen.getByText(text)).toBeTruthy();
	expect(screen.getByLabelText("Example.Read input")).toBeTruthy();
});

it("allows capability and step status labels to be replaced", () => {
	render(
		<>
			<CapabilityGuide
				capabilities={[
					{
						id: "1",
						title: "Example",
						description: "An action",
						status: "unreachable",
						reason: "No handler",
					},
				]}
				labels={{ unreachable: "Unavailable" }}
			/>
			<ScenarioSteps>
				<Step
					title="Wait"
					operation="Example.Wait"
					request={{}}
					state="pending"
					labels={{
						pending: "Queued",
						request: "Input",
						response: "Output",
						noResponse: "Waiting for output",
					}}
				/>
			</ScenarioSteps>
		</>,
	);
	for (const text of [
		"Unavailable",
		"Queued",
		"Input",
		"Output",
		"Waiting for output",
	])
		expect(screen.getByText(text)).toBeTruthy();
});
