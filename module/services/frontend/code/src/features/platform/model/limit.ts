/** Entitlement limits use the signed int64 wire range; -1 means unlimited. */
export function parseEntitlementLimit(value: string): bigint | null {
	if (!/^-?\d+$/.test(value.trim())) return null;
	const limit = BigInt(value.trim());
	return limit >= BigInt(-1) && limit <= BigInt("9223372036854775807")
		? limit
		: null;
}
