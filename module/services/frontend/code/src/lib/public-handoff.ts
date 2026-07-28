import { publicSiteConfig } from "@/generated/public-site-config";

type QueryReader = {
	get(name: string): string | null;
};

export function safePostLoginDestination(candidate: string): string {
	if (!candidate.startsWith("/") || candidate.startsWith("//")) return "/";
	try {
		const url = new URL(candidate, "https://product.invalid");
		if (url.origin !== "https://product.invalid") return "/";
		if (url.pathname.startsWith("/auth/")) return "/";
		return `${url.pathname}${url.search}`;
	} catch {
		return "/";
	}
}

export function publicHandoffDestination(query: QueryReader): string {
	const requestedDestination = query.get("next");
	if (requestedDestination) {
		const safeDestination = safePostLoginDestination(requestedDestination);
		if (safeDestination !== "/") return safeDestination;
	}

	const handoff = new URLSearchParams();
	const plan = query.get("plan")?.trim();
	if (plan && /^[a-z0-9][a-z0-9_-]{0,63}$/.test(plan)) {
		handoff.set("plan", plan);
	}
	for (const field of publicSiteConfig.acquisition.allowedAttribution) {
		const value = query.get(field)?.trim();
		if (value) handoff.set(field, value.slice(0, 200));
	}
	return handoff.size > 0 ? `/onboarding?${handoff.toString()}` : "/";
}
