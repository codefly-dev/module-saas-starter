// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { Field } from "../field.js";
import { Input } from "../input.js";
import { Pagination } from "../pagination.js";
import { SegmentedControl } from "../segmented-control.js";

afterEach(cleanup);

describe("SegmentedControl", () => {
	const options = [
		{ value: "list", label: "List" },
		{ value: "grid", label: "Grid" },
		{ value: "map", label: "Map", disabled: true },
	];

	it("marks the selected option and only that one", () => {
		render(
			<SegmentedControl
				options={options}
				value="grid"
				onValueChange={() => {}}
			/>,
		);
		expect(
			screen.getByRole("button", { name: "Grid" }).getAttribute("aria-pressed"),
		).toBe("true");
		expect(
			screen.getByRole("button", { name: "List" }).getAttribute("aria-pressed"),
		).toBe("false");
	});

	it("reports a change when another option is chosen", () => {
		const onValueChange = vi.fn();
		render(
			<SegmentedControl
				options={options}
				value="list"
				onValueChange={onValueChange}
			/>,
		);
		fireEvent.click(screen.getByRole("button", { name: "Grid" }));
		expect(onValueChange).toHaveBeenCalledWith("grid");
	});

	// A single-choice control has no empty state, so re-pressing the active
	// segment must be ignored rather than reported as a change to nothing.
	it("ignores a press on the option already selected", () => {
		const onValueChange = vi.fn();
		render(
			<SegmentedControl
				options={options}
				value="list"
				onValueChange={onValueChange}
			/>,
		);
		fireEvent.click(screen.getByRole("button", { name: "List" }));
		expect(onValueChange).not.toHaveBeenCalled();
	});

	it("does not report a change from a disabled option", () => {
		const onValueChange = vi.fn();
		render(
			<SegmentedControl
				options={options}
				value="list"
				onValueChange={onValueChange}
			/>,
		);
		fireEvent.click(screen.getByRole("button", { name: "Map" }));
		expect(onValueChange).not.toHaveBeenCalled();
	});

	it("sizes off the shared control rungs", () => {
		const { container } = render(
			<SegmentedControl
				options={options}
				value="list"
				onValueChange={() => {}}
				size="sm"
			/>,
		);
		expect(
			container.querySelector('[data-slot="segmented-control"]')?.className,
		).toContain("control-height-sm");
	});

	// The label is not rendered in icon-only mode, so the accessible name has to
	// come from somewhere else or the control is unusable without sight.
	it("keeps an accessible name when icons stand alone", () => {
		render(
			<SegmentedControl
				iconOnly
				options={[
					{
						value: "list",
						label: "List",
						icon: <svg />,
						"aria-label": "List view",
					},
				]}
				value="list"
				onValueChange={() => {}}
			/>,
		);
		expect(screen.getByRole("button", { name: "List view" })).toBeTruthy();
	});
});

describe("Pagination", () => {
	it("marks the current page and no other", () => {
		render(<Pagination page={3} pageCount={9} onPageChange={() => {}} />);
		expect(
			screen
				.getByRole("button", { name: "Page 3" })
				.getAttribute("aria-current"),
		).toBe("page");
		expect(
			screen
				.getByRole("button", { name: "Page 4" })
				.getAttribute("aria-current"),
		).toBeNull();
	});

	it("steps with the arrows", () => {
		const onPageChange = vi.fn();
		render(<Pagination page={3} pageCount={9} onPageChange={onPageChange} />);
		fireEvent.click(screen.getByRole("button", { name: "Next page" }));
		expect(onPageChange).toHaveBeenCalledWith(4);
		fireEvent.click(screen.getByRole("button", { name: "Previous page" }));
		expect(onPageChange).toHaveBeenCalledWith(2);
	});

	it("disables the arrow that would leave the collection", () => {
		const { unmount } = render(
			<Pagination page={1} pageCount={9} onPageChange={() => {}} />,
		);
		expect(
			(
				screen.getByRole("button", {
					name: "Previous page",
				}) as HTMLButtonElement
			).disabled,
		).toBe(true);
		unmount();
		render(<Pagination page={9} pageCount={9} onPageChange={() => {}} />);
		expect(
			(screen.getByRole("button", { name: "Next page" }) as HTMLButtonElement)
				.disabled,
		).toBe(true);
	});

	it("renders nothing when there is nothing to page", () => {
		const { container } = render(
			<Pagination page={1} pageCount={0} onPageChange={() => {}} />,
		);
		expect(container.querySelector('[data-slot="pagination"]')).toBeNull();
	});

	// The ellipsis is decoration: a screen reader announcing it between two page
	// numbers reads as a value, not as a gap.
	it("hides the gap from assistive technology", () => {
		const { container } = render(
			<Pagination page={20} pageCount={40} onPageChange={() => {}} />,
		);
		const gap = container.querySelector('[data-slot="pagination-ellipsis"]');
		expect(gap?.getAttribute("aria-hidden")).toBe("true");
	});
});

describe("Field", () => {
	it("ties the label to the control it labels", () => {
		render(<Field label="Email">{(control) => <Input {...control} />}</Field>);
		expect(screen.getByLabelText("Email")).toBeTruthy();
	});

	it("describes the control with its hint", () => {
		render(
			<Field label="Email" description="We never share it">
				{(control) => <Input {...control} />}
			</Field>,
		);
		const input = screen.getByLabelText("Email");
		const describedBy = input.getAttribute("aria-describedby");
		expect(describedBy).toBeTruthy();
		expect(
			describedBy
				?.split(" ")
				.map((id) => document.getElementById(id)?.textContent),
		).toContain("We never share it");
	});

	// An error must set `aria-invalid` as well as render, and must be announced:
	// a validation message that appears silently is one a screen-reader user
	// never learns about.
	it("marks the control invalid and announces the error", () => {
		render(
			<Field label="Email" error="That address is not valid">
				{(control) => <Input {...control} />}
			</Field>,
		);
		expect(screen.getByLabelText("Email").getAttribute("aria-invalid")).toBe(
			"true",
		);
		expect(screen.getByRole("alert").textContent).toBe(
			"That address is not valid",
		);
	});

	it("names the error before the hint, keeping both", () => {
		render(
			<Field label="Email" description="We never share it" error="Required">
				{(control) => <Input {...control} />}
			</Field>,
		);
		const ids =
			screen
				.getByLabelText("Email")
				.getAttribute("aria-describedby")
				?.split(" ") ?? [];
		expect(ids).toHaveLength(2);
		expect(document.getElementById(ids[0])?.textContent).toBe("Required");
	});

	it("leaves a clean field undescribed and valid", () => {
		render(<Field label="Email">{(control) => <Input {...control} />}</Field>);
		const input = screen.getByLabelText("Email");
		expect(input.getAttribute("aria-describedby")).toBeNull();
		expect(input.getAttribute("aria-invalid")).toBeNull();
	});

	it("gives two fields on one page distinct ids", () => {
		render(
			<>
				<Field label="First">{(control) => <Input {...control} />}</Field>
				<Field label="Second">{(control) => <Input {...control} />}</Field>
			</>,
		);
		expect(screen.getByLabelText("First").id).not.toBe(
			screen.getByLabelText("Second").id,
		);
	});
});
