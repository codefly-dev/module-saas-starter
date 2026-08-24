import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
	return twMerge(clsx(inputs));
}

export function formatDate(dateString: string | undefined): string {
	if (!dateString) return "-";
	try {
		return new Intl.DateTimeFormat("en-US", {
			year: "numeric",
			month: "short",
			day: "numeric",
			hour: "2-digit",
			minute: "2-digit",
		}).format(new Date(dateString));
	} catch {
		// Never return the raw input: callers render this straight into JSX, and
		// a non-string (e.g. a protobuf Timestamp object leaked past the model
		// boundary) would throw "Objects are not valid as a React child".
		return typeof dateString === "string" ? dateString : "-";
	}
}

export function truncateUUID(uuid: string): string {
	if (!uuid || uuid.length < 8) return uuid;
	return `${uuid.slice(0, 8)}...`;
}

export function formatLimit(limit: number | bigint): string {
	const n = Number(limit);
	if (n === -1) return "Unlimited";
	if (n === 0) return "Disabled";
	return n.toLocaleString();
}
