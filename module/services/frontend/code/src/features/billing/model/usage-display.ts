const zero = BigInt(0);
const ten = BigInt(10);
const hundred = BigInt(100);
const million = BigInt(1_000_000);

export function usagePercent(used: bigint, limit: bigint): number {
	if (limit <= zero || used <= zero) return 0;
	const rounded = (used * hundred + limit / BigInt(2)) / limit;
	return Number(rounded > hundred ? hundred : rounded);
}

export function usageTone(
	used: bigint,
	limit: bigint,
): "critical" | "warning" | "healthy" {
	if (limit > zero && used * ten > limit * BigInt(9)) return "critical";
	if (limit > zero && used * ten > limit * BigInt(7)) return "warning";
	return "healthy";
}

export function normalizeUsageSeries(values: bigint[]): number[] {
	if (values.length === 0) return [];
	const maximum = values.reduce(
		(current, value) => (value > current ? value : current),
		zero,
	);
	if (maximum <= BigInt(Number.MAX_SAFE_INTEGER)) {
		return values.map(Number);
	}
	return values.map((value) => Number((value * million) / maximum));
}

export function projectUsage(
	used: bigint,
	periodMilliseconds: number,
	elapsedMilliseconds: number,
): bigint | undefined {
	if (
		periodMilliseconds <= 0 ||
		elapsedMilliseconds <= 0 ||
		!Number.isSafeInteger(periodMilliseconds) ||
		!Number.isSafeInteger(elapsedMilliseconds)
	) {
		return undefined;
	}
	const period = BigInt(periodMilliseconds);
	const elapsed = BigInt(elapsedMilliseconds);
	return (used * period + elapsed / BigInt(2)) / elapsed;
}

export function usageHistoryPresentation(
	isLoading: boolean,
	isError: boolean,
	pointCount: number,
	used: bigint,
): "loading" | "partial" | "chart" | "no_data" | "ready" {
	if (isLoading) return "loading";
	if (isError) return "partial";
	if (pointCount > 1) return "chart";
	if (used === zero) return "no_data";
	return "ready";
}
