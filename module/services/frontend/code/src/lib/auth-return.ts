export function safeReturnPath(candidate: string | null | undefined): string {
	if (!candidate?.startsWith("/") || candidate.startsWith("//")) {
		return "/";
	}
	if (
		candidate.includes("\\") ||
		Array.from(candidate).some((character) => character.charCodeAt(0) < 32)
	) {
		return "/";
	}
	try {
		const parsed = new URL(candidate, "https://return.invalid");
		if (parsed.origin !== "https://return.invalid") return "/";
		return `${parsed.pathname}${parsed.search}${parsed.hash}`;
	} catch {
		return "/";
	}
}
