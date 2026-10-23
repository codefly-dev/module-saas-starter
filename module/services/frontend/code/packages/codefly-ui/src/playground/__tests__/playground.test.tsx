// @vitest-environment happy-dom
import {
	cleanup,
	fireEvent,
	render,
	screen,
	within,
} from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { renderToString } from "react-dom/server";
import { FixtureScenario } from "../../../stories/playground.stories.js";
import { OperationLog, ScenarioSteps, Step } from "../index.js";

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
