import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
	return twMerge(clsx(inputs));
}

export function formatDate(
	dateString: string | { seconds: bigint; nanos: number } | undefined,
): string {
	if (!dateString) return "-";
	try {
		return new Intl.DateTimeFormat("en-US", {
			year: "numeric",
			month: "short",
			day: "numeric",
			hour: "2-digit",
			minute: "2-digit",
		}).format(
			new Date(
				typeof dateString === "string"
					? dateString
					: Number(dateString.seconds) * 1000 + dateString.nanos / 1e6,
			),
		);
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
	if (limit === -1 || limit === BigInt(-1)) return "Unlimited";
	if (limit === 0 || limit === BigInt(0)) return "Disabled";
	return limit.toLocaleString();
}
