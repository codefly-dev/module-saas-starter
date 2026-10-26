// @vitest-environment happy-dom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { QRCode, type QRCodeProps } from "../qr-code.js";

afterEach(cleanup);

/**
 * The real shape of what this renders in the product: `totpSecretBytes = 32` in
 * the accounts service, base32-encoded without padding, is 52 characters — not
 * the 16- or 32-character secret a hand-written fixture reaches for. A shorter
 * one encodes to a smaller, sparser grid, so a test written against it would
 * clear a density floor the product never meets.
 */
const SECRET = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXPJBSW";
const OTPAUTH = `otpauth://totp/SaaSStarter:jane.doe@example.com?secret=${SECRET}&issuer=SaaSStarter&digits=6&period=30`;
/** A long address at `M`, which encodes to the same grid as `OTPAUTH` at `H`. */
const OTPAUTH_LONG_ADDRESS = `otpauth://totp/SaaSStarter:jane.doe.van-der-berg@engineering.example-corporation.com?secret=${SECRET}&issuer=SaaSStarter&digits=6&period=30`;

const SIZES: NonNullable<QRCodeProps["size"]>[] = ["md", "lg"];
/** Must match `MIN_PX_PER_MODULE` in the component. This file is the gate. */
const MIN_PX_PER_MODULE = 2;

/** The square grid the SVG draws, quiet zone included. */
function cells(container: HTMLElement): number {
	const viewBox = container.querySelector("svg")?.getAttribute("viewBox");
	return Number(viewBox?.split(" ")[3]);
}

/**
 * The plate's usable edge in CSS pixels, read off the classes it actually
 * carries rather than from a constant restated here — an edge changed in the
 * component without its floor being rechecked has to fail, not pass.
 */
function usableEdgePx(container: HTMLElement): number {
	const className =
		container.querySelector("[data-slot=qr-code]")?.className ?? "";
	const edge = className.match(/size-\[([\d.]+)rem\]/);
	const padding = className.match(/p-\[([\d.]+)rem\]/);
	expect(edge, `no absolute size utility in "${className}"`).toBeTruthy();
	expect(padding, `no absolute padding utility in "${className}"`).toBeTruthy();
	return (Number(edge?.[1]) - 2 * Number(padding?.[1])) * 16;
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

	describe("every size it ships keeps a module wide enough to scan", () => {
		// The promise in the `size` doc comment, held to the densest payload this
		// is built for. Before this, `sm` (a 128px plate) drew the real enrolment
		// URI at 1.82px per module and the comment said it would scan.
		for (const size of SIZES) {
			for (const [name, value, level] of [
				["a real enrolment URI", OTPAUTH, "M"],
				["the same at the highest recovery level", OTPAUTH, "H"],
				["a long address", OTPAUTH_LONG_ADDRESS, "M"],
			] as const) {
				it(`${size}, ${name}`, () => {
					const { container } = render(
						<QRCode value={value} label="Scan me" size={size} level={level} />,
					);

					const pxPerModule = usableEdgePx(container) / cells(container);
					expect(
						pxPerModule,
						`${size} draws ${cells(container)} modules across ${usableEdgePx(container)}px — ${pxPerModule.toFixed(2)}px each`,
					).toBeGreaterThanOrEqual(MIN_PX_PER_MODULE);
				});
			}
		}
	});

	it("sizes the plate in absolute units, which a density skin cannot rescale", () => {
		// `size-56`/`p-3` resolve through `--spacing`, which a skin sets to choose
		// density. That is right for a control and wrong here: a skin at 0.15rem
		// would take the lg plate from 224px to 134px and every module below the
		// floor, turning a reskin into a code that does not scan.
		const { container } = render(
			<QRCode value={OTPAUTH} label="Scan me" size="lg" />,
		);

		const className =
			container.querySelector("[data-slot=qr-code]")?.className ?? "";
		expect(className).toMatch(/size-\[[\d.]+rem\]/);
		expect(className).not.toMatch(/(?:^|\s)(?:size|h|w|p)-\d/);
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

	it("draws nothing at all rather than a code that scans to nothing", () => {
		// The caller mistake this guards is rendering before the data arrives,
		// which is the shape the MFA dialog had: an empty value encodes to a
		// perfectly valid code that an authenticator reads and then rejects.
		const { container } = render(<QRCode value="" label="Not ready yet" />);

		expect(container.querySelector("[data-slot=qr-code]")).toBeNull();
		expect(screen.queryByRole("img")).toBeNull();
	});

	it("takes the caller's box, so a plate can be sized past the two steps", () => {
		const { container } = render(
			<QRCode value={OTPAUTH} label="Scan me" className="size-[20rem]" />,
		);

		const plate = container.querySelector("[data-slot=qr-code]");
		expect(plate?.className).toContain("size-[20rem]");
		// The SVG fills whatever plate it is given rather than its own attribute.
		expect(container.querySelector("svg")?.getAttribute("class")).toContain(
			"h-full w-full",
		);
	});
});
