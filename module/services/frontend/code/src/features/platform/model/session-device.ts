/** A readable device summary. Never put an unbounded user-agent in a table. */
export function sessionDevice(device: Record<string, string>): string {
	const agent =
		device.browser ||
		device.userAgent ||
		device.user_agent ||
		device.description ||
		"";
	const browser = /Edg(?:e|A|iOS)?\//.test(agent)
		? "Edge"
		: /(?:Firefox|FxiOS)\//.test(agent)
			? "Firefox"
			: /(?:Chrome|CriOS)\//.test(agent)
				? "Chrome"
				: /Safari\//.test(agent)
					? "Safari"
					: ["Chrome", "Firefox", "Safari", "Edge"].includes(agent)
						? agent
						: "";
	const os = /Android/.test(agent)
		? "Android"
		: /iPhone|iPad/.test(agent)
			? "iOS"
			: /Windows/.test(agent)
				? "Windows"
				: /Macintosh|Mac OS/.test(agent)
					? "macOS"
					: /Linux/.test(agent)
						? "Linux"
						: ["Windows", "macOS", "Linux", "Android", "iOS"].includes(
									device.os,
								)
							? device.os
							: "";
	return [browser, os].filter(Boolean).join(" on ") || "Unknown device";
}
