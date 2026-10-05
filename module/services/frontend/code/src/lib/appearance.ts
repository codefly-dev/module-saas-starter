export {
	type AppearanceStyleProperties,
	appearanceStyleProperties,
	appearanceVariableName,
} from "@codefly-dev/ui/skin";

export function readableForeground(hexColor: string): "#000000" | "#ffffff" {
	const channels = [1, 3, 5].map(
		(offset) => Number.parseInt(hexColor.slice(offset, offset + 2), 16) / 255,
	);
	const linear = channels.map((channel) =>
		channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4,
	);
	const luminance =
		0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
	return luminance > 0.179 ? "#000000" : "#ffffff";
}
