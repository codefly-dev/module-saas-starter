import { QRCodeSVG } from "qrcode.react";
import { cn } from "./cn.js";

export interface QRCodeProps {
	/**
	 * What a scanner should read: an `otpauth://` enrolment URI, a URL, a pairing
	 * token. Encoded verbatim — this adds no prefix and no escaping of its own.
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
	 * The plate's edge. A code has to survive a phone camera held at arm's length,
	 * so these are the three sizes that do: `sm` for a list row, `md` for a
	 * panel, `lg` for the one thing a dialog is asking the reader to scan.
	 */
	size?: "sm" | "md" | "lg";
	/**
	 * Error-correction level — how much of the code may be obscured and still
	 * decode. `M` (~15%) is what authenticator enrolment uses; `Q`/`H` buy
	 * tolerance for a printed or partly covered code at the cost of denser
	 * modules.
	 */
	level?: "L" | "M" | "Q" | "H";
	className?: string;
}

const plateSize: Record<NonNullable<QRCodeProps["size"]>, string> = {
	sm: "size-32",
	md: "size-44",
	lg: "size-56",
};

/**
 * A scannable QR code on its own plate.
 *
 * The plate is deliberately the one place in this kit that names a colour
 * instead of a token. A QR code's contrast is not a design decision a skin may
 * re-make: the format assumes dark modules on a light field, and a reader that
 * meets an inverted code may simply fail to decode it — which, on an enrolment
 * screen, reads to the user as the product being broken. So the plate stays
 * light and the modules stay dark in both modes, and the surrounding surface is
 * the skin's to colour.
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
	return (
		<div
			data-slot="qr-code"
			className={cn(
				"inline-flex shrink-0 items-center justify-center rounded-lg border border-border bg-white p-3",
				plateSize[size],
				className,
			)}
		>
			{/* The box sizes the code, not the `size` prop: a CSS width beats the
			    width attribute, so the SVG fills whatever plate it is given and a
			    caller's `className` can override the three steps above. */}
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
