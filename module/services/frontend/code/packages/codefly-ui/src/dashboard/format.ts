// Default axis-label formatters, pure and DOM-free so they unit-test directly.
// Kept out of the geometry (which only knows numbers) because labels are a
// presentation concern the Axis atom owns; callers can override either.

// Compact value labels for the y axis: "1.2K", "3", "0.5", "0.02". Compact
// notation keeps a tick from blowing out the left gutter once counts reach the
// thousands. Bounding *significant* digits (not fraction digits) is what keeps a
// small fractional metric legible: `avg`/`ratio`/`percentile` series can tick in
// the hundredths, and a fraction-digit cap would round every such tick to "0".
const valueFormat = new Intl.NumberFormat(undefined, { notation: "compact", maximumSignificantDigits: 3 });

export function formatAxisValue(value: number): string {
	return valueFormat.format(value);
}

// Series keys that are time buckets arrive as ISO timestamps from the audit RPC
// (`YYYY-MM-DDThh:mm:ss±hh`); anything else is a plain category key. Gate on the
// ISO shape so a non-date key ("US East", "42") is never coerced through Date.
const ISO_TIME = /^\d{4}-\d{2}-\d{2}([T ]\d{2}:\d{2}(:\d{2})?)?([+-]\d{2}(:?\d{2})?|Z)?$/;

// Postgres' `OF` offset emits a bare-hour zone ("+00", "-05") that Date.parse
// rejects; pad it to the "+00:00" the parser accepts. A "+0000" form gets its
// colon too. The `:` guard scopes this to timestamps — without it the "-25" of a
// date-only "2026-12-25" would be misread as an offset and mangled.
function normalizeOffset(iso: string): string {
	if (!iso.includes(":")) return iso;
	return iso.replace(/([+-]\d{2})(\d{2})?$/, (_m, hh: string, mm?: string) => `${hh}:${mm ?? "00"}`);
}

// Time buckets are day/week/month grains, so a key at exact UTC midnight reads as
// a date ("Sep 1"); a finer bucket keeps its time ("Sep 1, 14:00"). Formatting in
// UTC matches the bucket boundary the server truncated to and keeps the label
// stable regardless of the viewer's zone.
export function formatAxisKey(key: string): string {
	const trimmed = key.trim();
	if (!ISO_TIME.test(trimmed)) return key;
	const ms = Date.parse(normalizeOffset(trimmed));
	if (Number.isNaN(ms)) return key;
	const date = new Date(ms);
	const midnight = date.getUTCHours() === 0 && date.getUTCMinutes() === 0;
	return date.toLocaleString(undefined, {
		timeZone: "UTC",
		month: "short",
		day: "numeric",
		...(midnight ? {} : { hour: "2-digit", minute: "2-digit" }),
	});
}
