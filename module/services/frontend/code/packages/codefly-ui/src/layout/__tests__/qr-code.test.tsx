// @vitest-environment happy-dom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { QRCode } from "../qr-code.js";

afterEach(cleanup);

const OTPAUTH =
	"otpauth://totp/Example:jane.doe@example.com?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=Example&digits=6&period=30";

/** The square grid the SVG draws, quiet zone included. */
function cells(container: HTMLElement): number {
	const viewBox = container.querySelector("svg")?.getAttribute("viewBox");
	return Number(viewBox?.split(" ")[3]);
}

describe("QRCode", () => {
	it("is an image named by what scanning it would do", () => {
		render(<QRCode value={OTPAUTH} label="Scan to add this account" />);

		const image = screen.getByRole("img", {
			name: "Scan to add this account",
		});
		expect(image.tagName.toLowerCase()).toBe("svg");
	});

	it("encodes the value rather than drawing a fixed figure", () => {
		// A placeholder, or a value that never reached the encoder, would draw the
		// same grid for both of these. The short one is a version-1 code: 21
		// modules, so 29 cells with the quiet zone, which also pins the quiet zone
		// at the specified four modules a side.
		const { container: short } = render(<QRCode value="HELLO" label="Short" />);
		expect(cells(short)).toBe(29);

		const { container: long } = render(
			<QRCode value={OTPAUTH} label="An enrolment URI" />,
		);
		expect(cells(long)).toBeGreaterThan(cells(short));
	});

	it("raises the version rather than the density when asked for more recovery", () => {
		const { container: medium } = render(
			<QRCode value={OTPAUTH} label="At M" level="M" />,
		);
		const { container: high } = render(
			<QRCode value={OTPAUTH} label="At H" level="H" />,
		);

		expect(cells(high)).toBeGreaterThan(cells(medium));
	});

	it("keeps dark modules on a light plate whatever the skin says", () => {
		// Pinned deliberately, as the component's own comment explains: a QR
		// reader may refuse an inverted code, so these two are the one place in
		// the kit where contrast is not the skin's to re-decide. A change here
		// should be a change someone argued for, not a reskin that swept through.
		const { container } = render(<QRCode value={OTPAUTH} label="Scan me" />);

		expect(container.querySelector("[data-slot=qr-code]")?.className).toContain(
			"bg-white",
		);
		const paths = [...container.querySelectorAll("svg path")];
		expect(paths.at(-1)?.getAttribute("fill")).toBe("#000000");
	});

	it("takes the caller's box, so a plate can be sized past the three steps", () => {
		const { container } = render(
			<QRCode value={OTPAUTH} label="Scan me" className="size-96" />,
		);

		const plate = container.querySelector("[data-slot=qr-code]");
		expect(plate?.className).toContain("size-96");
		// The SVG fills whatever plate it is given rather than its own attribute.
		expect(container.querySelector("svg")?.getAttribute("class")).toContain(
			"h-full w-full",
		);
	});
});
