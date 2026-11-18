import { type Timestamp, timestampDate } from "@bufbuild/protobuf/wkt";
import { formatDate } from "@/shared/lib/utils";

// What a source's schedule, the access its runs borrow and the runs themselves
// say, in an organization administrator's words. The platform answers in the
// spellings its own documents carry (a five-field schedule expression, policy
// values, permission names, nanoseconds); none of those is shown as it comes.
// A value this file does not know reads as a plain sentence, never as itself.

const plural = (n: number, one: string, many: string) =>
	n === 1 ? `Every ${one}` : `Every ${n} ${many}`;

const two = (n: number) => String(n).padStart(2, "0");

const DAYS = [
	"Sunday",
	"Monday",
	"Tuesday",
	"Wednesday",
	"Thursday",
	"Friday",
	"Saturday",
];

const number = (field: string) =>
	/^\d+$/.test(field) ? Number(field) : undefined;
const step = (field: string) => {
	const match = /^\*\/(\d+)$/.exec(field);
	return match && Number(match[1]) > 0 ? Number(match[1]) : undefined;
};

/** How often a schedule runs. An expression not read here is "custom". */
export function scheduleWords(cron: string | undefined): string {
	const fields = (cron ?? "").trim().split(/\s+/);
	if (!cron || fields.length !== 5) return "Not scheduled";
	const [minute, hour, day, month, weekday] = fields;
	if (day === "*" && month === "*") {
		const at = number(minute);
		const on = number(hour);
		if (weekday === "*") {
			const minutes = step(minute);
			if (minutes && hour === "*") return plural(minutes, "minute", "minutes");
			if (minute === "*" && hour === "*") return "Every minute";
			if (at !== undefined && hour === "*") return "Every hour";
			const hours = step(hour);
			if (at !== undefined && hours) return plural(hours, "hour", "hours");
			if (at !== undefined && on !== undefined && on < 24 && at < 60)
				return `Every day at ${two(on)}:${two(at)}`;
		}
		const weekly = number(weekday);
		if (
			weekly !== undefined &&
			weekly <= 7 &&
			at !== undefined &&
			on !== undefined &&
			on < 24 &&
			at < 60
		)
			return `Every ${DAYS[weekly % 7]} at ${two(on)}:${two(at)}`;
	}
	return "On a custom schedule";
}

/** What happens to a run whose time passed while nothing could run it. */
export function missedRunWords(policy: string | undefined): string {
	switch (policy) {
		case "skip":
			return "Skipped. The next run happens at its usual time.";
		case "catch_up":
			return "Run as soon as possible afterwards.";
		default:
			return "Not specified";
	}
}

/** What happens when a run comes due while the previous one is still going. */
export function overlapWords(policy: string | undefined): string {
	switch (policy) {
		case "skip":
			return "The new run is skipped.";
		case "queue":
			return "The new run waits for the previous one to finish.";
		case "cancel_previous":
			return "The previous run is stopped and the new one starts.";
		default:
			return "Not specified";
	}
}

/** How long one run may take. The platform counts it in nanoseconds. */
export function timeLimitWords(nanos: bigint | undefined): string {
	// BigInt literals need ES2020 and this project targets lower.
	if (nanos === undefined || nanos <= BigInt(0)) return "No limit set";
	const seconds = Number(nanos / BigInt(1_000_000_000));
	const count = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`;
	if (seconds >= 3600 && seconds % 3600 === 0)
		return count(seconds / 3600, "hour");
	if (seconds >= 60 && seconds % 60 === 0) return count(seconds / 60, "minute");
	return count(seconds, "second");
}

/** What the scheduled work does. */
export function workWords(operation: string | undefined): string {
	if (operation?.endsWith("/ReconcileSource"))
		return "Refreshes this data source so its content stays up to date";
	return "Scheduled work for this data source";
}

/**
 * Whose access a run is set to use. Neither is the state in which a run is
 * refused rather than done as the module itself, so it is said outright.
 */
export function runsWithWords(
	identity: { source: string; binding: string } | undefined,
): string {
	if (identity?.source) return "A person's access, listed below";
	if (identity?.binding) return "The module's own access";
	return "Nobody's access, so runs are refused";
}

const VERBS: Record<string, string> = {
	read: "View",
	write: "Change",
	delete: "Delete",
	invoke: "Run",
	admin: "Manage",
	manage: "Manage",
};

/** One permission, as what it lets the work do. */
export function permissionWords(scope: string): string {
	const [resource, verb, ...rest] = scope.split(":");
	const act = VERBS[verb ?? ""];
	if (
		!act ||
		!resource ||
		rest.length > 0 ||
		!/^[a-z][a-z0-9_-]*$/.test(resource)
	)
		return "Other access";
	return `${act} ${resource.replace(/[_-]+/g, " ")}`;
}

/** Everything an access allows, as one line. */
export function permissionsWords(scopes: readonly string[]): string {
	const said = [...new Set(scopes.map(permissionWords))];
	return said.length > 0 ? said.join(", ") : "Nothing";
}

/** What a recorded run was for. */
export function runKindWords(lookup: boolean): string {
	return lookup ? "Checked the result of an earlier run" : "Did the work";
}

/** One moment, or `absent` when it never happened. */
export function momentWords(value: Timestamp | undefined, absent: string) {
	return value ? formatDate(timestampDate(value).toISOString()) : absent;
}
