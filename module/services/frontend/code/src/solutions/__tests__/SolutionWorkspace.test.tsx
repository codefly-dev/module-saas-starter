import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { useEffect, useState } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { SolutionSettings, SolutionWorkspace } from "../SolutionWorkspace";

afterEach(cleanup);

it("opens settings without remounting or discarding the working draft", async () => {
	const mount = vi.fn();
	function Work() {
		const [value, setValue] = useState("");
		useEffect(() => {
			mount();
		}, []);
		return (
			<input
				aria-label="Working draft"
				value={value}
				onChange={(event) => setValue(event.target.value)}
			/>
		);
	}
	render(
		<SolutionWorkspace
			title="Example"
			settings={
				<SolutionSettings title="Example">
					<p>Existing setup</p>
				</SolutionSettings>
			}
		>
			<Work />
		</SolutionWorkspace>,
	);
	fireEvent.change(screen.getByRole("textbox", { name: "Working draft" }), {
		target: { value: "Keep this correction" },
	});
	expect(screen.queryByRole("tab")).toBeNull();
	fireEvent.click(screen.getByRole("button", { name: "Settings for Example" }));
	expect(
		await screen.findByRole("dialog", { name: "Example settings" }),
	).toBeTruthy();
	fireEvent.click(screen.getByRole("button", { name: /^Close$/ }));
	await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	expect(
		(screen.getByRole("textbox", { name: "Working draft" }) as HTMLInputElement)
			.value,
	).toBe("Keep this correction");
	expect(mount).toHaveBeenCalledTimes(1);
});

it("keeps work mounted when visiting an optional dashboard", () => {
	render(
		<SolutionWorkspace title="Example" dashboard={<p>Declared dashboard</p>}>
			<input aria-label="Working draft" defaultValue="Keep this" />
		</SolutionWorkspace>,
	);
	const draft = screen.getByRole("textbox", { name: "Working draft" });
	fireEvent.click(screen.getByRole("tab", { name: "Dashboard" }));
	expect(screen.getByText("Declared dashboard")).toBeTruthy();
	fireEvent.click(screen.getByRole("tab", { name: "Workspace" }));
	expect(screen.getByRole("textbox", { name: "Working draft" })).toBe(draft);
});
