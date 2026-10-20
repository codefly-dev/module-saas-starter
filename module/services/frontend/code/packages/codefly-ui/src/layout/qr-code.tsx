import { QRCodeSVG } from "qrcode.react";
import { cn } from "./cn.js";

export interface QRCodeProps {
	/**
	 * What a scanner should read: an `otpauth://` enrolment URI, a URL, a pairing
	 * token. Encoded verbatim — this adds no prefix and no escaping of its own.
	 * An empty value draws nothing: a code of the empty string is perfectly
	 * scannable and means nothing, which is worse than an absent one.
	 */
	value: string;
	/**
	 * Required: what scanning this would accomplish, as a sentence. A QR code is
	 * a picture of a string, so a screen-reader user gets nothing from the image
	 * itself — the name has to say what it is for, and the caller is expected to
	 * offer the same `value` in a form that can be read and copied beside it.
	 */
	label: string;
	/**
	 * The plate's edge: `md` for a panel, `lg` for the one thing a dialog is
	 * asking the reader to scan. Both keep every module at least two CSS pixels
	 * wide for the payloads described below, which is what makes them scannable
	 * rather than merely visible.
	 */
	size?: "md" | "lg";
	/**
	 * Error-correction level — how much of the code may be obscured and still
	 * decode. `M` (~15%) is what authenticator enrolment uses; `Q`/`H` buy
	 * tolerance for a printed or partly covered code, at the cost of more
	 * modules in the same plate.
	 */
	level?: "L" | "M" | "Q" | "H";
	className?: string;
}

/**
 * Plate geometry in absolute units, which is the second place this component
 * declines a token (the first is colour, below).
 *
 * Every other size in the kit is a multiple of `--spacing`, which a skin
 * rescales to set density — and that is right for a control, whose size is a
 * design decision. It is wrong here: a module small enough to defeat a camera
 * is not a denser look, it is a code that does not scan, and a density skin
 * would silently take any spacing-based plate below the floor. So the edge and
 * the framing padding are stated in `rem`, which follows the reader's own text
 * size and nothing else.
 *
 * The floor is two CSS pixels per module — roughly what a camera needs to
 * resolve one at arm's length. Dividing the usable area (`edge` less twice the
 * padding) by it, `md` admits a grid of 84 modules and `lg` one of 100. The
 * densest payload this is built for — a 145-character `otpauth://` URI at level
 * `H`, or a 182-character one at `M` — is 73. `qr-code.test.tsx` is where that
 * floor is enforced, against the plate this actually renders rather than
 * against the numbers restated here.
 */
const PLATE_PADDING = "p-[0.75rem]";
const plateSize: Record<NonNullable<QRCodeProps["size"]>, string> = {
	md: "size-[12rem]",
	lg: "size-[14rem]",
};

/**
 * A scannable QR code on its own plate.
 *
 * The plate is deliberately the one place in this kit that names a colour
 * instead of a token. A QR code's contrast is not a design decision a
 * skin may re-make: the format assumes dark modules on a light field, and a
 * reader that meets an inverted code may simply fail to decode it — which, on
 * an enrolment screen, reads to the user as the product being broken. So the
 * plate stays light and the modules stay dark in both modes, and the
 * surrounding surface is the skin's to colour.
 *
 * The quiet zone is the specified four modules and lives inside the SVG, so it
 * scales with the code and survives a screenshot cropped to the image.
 */
export function QRCode({
	value,
	label,
	size = "md",
	level = "M",
	className,
}: QRCodeProps) {
	// Nothing to scan is better than something that scans to nothing: a caller
	// rendering before its data arrives would otherwise put a valid code for the
	// empty string in front of the reader, which their authenticator rejects
	// without saying why.
	if (value === "") return null;
	return (
		<div
			data-slot="qr-code"
			className={cn(
				"inline-flex shrink-0 items-center justify-center rounded-lg border border-border bg-white",
				PLATE_PADDING,
				plateSize[size],
				className,
			)}
		>
			{/* The box sizes the code, not the `size` prop: a CSS width beats the
			    width attribute, so the SVG fills whatever plate it is given and a
			    caller's `className` can override the two steps above. */}
			<QRCodeSVG
				value={value}
				title={label}
				level={level}
				marginSize={4}
				bgColor="transparent"
				fgColor="#000000"
				className="h-full w-full"
			/>
		</div>
	);
}
