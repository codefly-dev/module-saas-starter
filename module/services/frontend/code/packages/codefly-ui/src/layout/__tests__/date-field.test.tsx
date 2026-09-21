import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";

import { DateField, type DateFieldVariant } from "../date-field.js";

function Harness({
	variant,
	initial = "",
}: {
	variant?: DateFieldVariant;
	initial?: string;
}) {
	const [value, setValue] = useState(initial);
	return (
		<>
			<DateField
				variant={variant}
				label="Document date"
				value={value}
				onValueChange={setValue}
			/>
			<output data-testid="value">{value}</output>
		</>
	);
}

describe("DateField", () => {
	afterEach(cleanup);

	it("captions the parts variant with a legend, not a label", () => {
		render(<Harness />);
		// One legend names the group; each input keeps its own label. A single
		// `htmlFor` cannot name three controls, which is the whole reason this
		// variant uses a fieldset.
		expect(screen.getByRole("group", { name: /document date/i })).toBeTruthy();
		for (const part of ["Day", "Month", "Year"]) {
			expect(screen.getByLabelText(part)).toBeTruthy();
		}
	});

	it("emits an ISO date only once every part is present", () => {
		render(<Harness />);
		const out = () => screen.getByTestId("value").textContent;

		fireEvent.change(screen.getByLabelText("Day"), { target: { value: "27" } });
		expect(out()).toBe("");
		fireEvent.change(screen.getByLabelText("Month"), {
			target: { value: "3" },
		});
		expect(out()).toBe("");

		fireEvent.change(screen.getByLabelText("Year"), {
			target: { value: "2007" },
		});
		expect(out()).toBe("2007-03-27");
	});

	it("keeps what the person typed rather than reformatting it", () => {
		render(<Harness />);
		// A single-digit month stays single-digit in the box the person is
		// looking at, even though the emitted ISO value pads it.
		fireEvent.change(screen.getByLabelText("Month"), {
			target: { value: "3" },
		});
		expect((screen.getByLabelText("Month") as HTMLInputElement).value).toBe(
			"3",
		);
	});

	it("refuses digits the part cannot hold and non-digits entirely", () => {
		render(<Harness />);
		fireEvent.change(screen.getByLabelText("Day"), {
			target: { value: "999" },
		});
		expect((screen.getByLabelText("Day") as HTMLInputElement).value).toBe("99");
		fireEvent.change(screen.getByLabelText("Year"), {
			target: { value: "20a7" },
		});
		expect((screen.getByLabelText("Year") as HTMLInputElement).value).toBe(
			"207",
		);
	});

	it("reports an out-of-range part as absent rather than as a wrong date", () => {
		render(<Harness />);
		fireEvent.change(screen.getByLabelText("Day"), { target: { value: "27" } });
		fireEvent.change(screen.getByLabelText("Month"), {
			target: { value: "13" },
		});
		fireEvent.change(screen.getByLabelText("Year"), {
			target: { value: "2007" },
		});
		expect(screen.getByTestId("value").textContent).toBe("");
	});

	it("splits an incoming ISO value back into its parts", () => {
		render(<Harness initial="2007-03-27" />);
		expect((screen.getByLabelText("Day") as HTMLInputElement).value).toBe("27");
		expect((screen.getByLabelText("Month") as HTMLInputElement).value).toBe(
			"03",
		);
		expect((screen.getByLabelText("Year") as HTMLInputElement).value).toBe(
			"2007",
		);
	});

	it("marks only the parts an error names", () => {
		render(
			<DateField
				label="Document date"
				value=""
				onValueChange={() => {}}
				error="The month must be between 1 and 12."
				errorParts={["month"]}
			/>,
		);
		expect(screen.getByLabelText("Month").getAttribute("aria-invalid")).toBe(
			"true",
		);
		expect(
			screen.getByLabelText("Day").getAttribute("aria-invalid"),
		).toBeNull();
		expect(screen.getByRole("alert").textContent).toBe(
			"The month must be between 1 and 12.",
		);
	});

	it("renders the compact variant as one labelled native date control", () => {
		render(<Harness variant="compact" />);
		const control = screen.getByLabelText(/document date/i);
		expect(control.getAttribute("type")).toBe("date");
		expect(screen.queryByLabelText("Day")).toBeNull();
	});

	it("carries the same ISO value in both variants", () => {
		render(<Harness variant="compact" />);
		const control = screen.getByLabelText(/document date/i);
		fireEvent.change(control, { target: { value: "2007-03-27" } });
		expect(screen.getByTestId("value").textContent).toBe("2007-03-27");
	});
});
