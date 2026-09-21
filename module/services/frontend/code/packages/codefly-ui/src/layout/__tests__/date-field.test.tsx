import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { useState } from "react";
import { renderToString } from "react-dom/server";
import { afterEach, describe, expect, it } from "vitest";

import { DateField, type DateFieldVariant } from "../date-field.js";

const DATE_PATTERN_PROPERTY = "--appearance-date-pattern";

/**
 * Set the pattern the way the appearance projection does — an inline custom
 * property on <html> — rather than by reaching into the component. That is the
 * channel `appearanceStyleProperties` writes, so a test that stopped matching
 * it would stop testing the skin.
 */
function setSkinDatePattern(pattern: DateFieldVariant): void {
	document.documentElement.style.setProperty(DATE_PATTERN_PROPERTY, pattern);
}

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
	afterEach(() => {
		cleanup();
		// Every test that asserts the default shape depends on no skin having
		// been left behind on <html>.
		document.documentElement.style.removeProperty(DATE_PATTERN_PROPERTY);
	});

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

	// The point of the feature: one call site, and the deployment's skin decides
	// the shape. Without these the mechanism could regress to always-parts and
	// every other test here would still pass.
	it("takes the skin's pattern when the call site names no variant", () => {
		setSkinDatePattern("compact");
		render(<Harness />);
		expect(screen.getByLabelText(/document date/i).getAttribute("type")).toBe(
			"date",
		);
		expect(screen.queryByLabelText("Day")).toBeNull();
	});

	it("lets an explicit variant outrank the skin", () => {
		// A screen that must pin one shape keeps it even under the other skin.
		setSkinDatePattern("compact");
		const { container } = render(<Harness variant="parts" />);
		expect(screen.getByLabelText("Day")).toBeTruthy();
		// The legend names the fieldset, so the group answers to the label too;
		// the native control's absence is what distinguishes the shapes.
		expect(container.querySelector('input[type="date"]')).toBeNull();
	});

	it("follows the skin when it is swapped in place", async () => {
		// Why this control subscribes rather than reading once: the explorer
		// swaps skins live, and a control that read at mount would keep the old
		// shape until a reload.
		render(<Harness />);
		expect(screen.getByLabelText("Day")).toBeTruthy();

		setSkinDatePattern("compact");

		await waitFor(() => {
			expect(screen.queryByLabelText("Day")).toBeNull();
		});
		expect(screen.getByLabelText(/document date/i).getAttribute("type")).toBe(
			"date",
		);
	});

	it("renders the parts shape on the server, whatever the skin says", () => {
		// The server snapshot cannot consult the skin: the pattern travels as a
		// CSS custom property on <html>, and there is no computed style without
		// a DOM. So SSR emits the parts shape and a `compact` deployment flips
		// to its real shape on hydration. Pinned because it is a real edge of
		// the current design, not because it is desirable — the PR's follow-up
		// covers giving the server the resolved pattern directly.
		setSkinDatePattern("compact");
		const html = renderToString(
			<DateField label="Document date" value="" onValueChange={() => {}} />,
		);
		expect(html).toContain('data-variant="parts"');
		expect(html).not.toContain('type="date"');
	});
});
