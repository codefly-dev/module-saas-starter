export type ErrorTrackingMode = "disabled" | "sentry";

export function configuredErrorTracking(
	rawMode: string | undefined,
	dsn: string | undefined,
): { enabled: boolean; dsn?: string } {
	const mode = (rawMode ?? "disabled").trim().toLowerCase();
	if (mode === "disabled") {
		if (dsn?.trim()) {
			throw new Error(
				"Sentry DSN is present while ERROR_TRACKING_MODE is disabled",
			);
		}
		return { enabled: false };
	}
	if (mode !== "sentry") {
		throw new Error("ERROR_TRACKING_MODE must be disabled or sentry");
	}
	const normalizedDSN = dsn?.trim();
	if (!normalizedDSN) {
		throw new Error("Sentry DSN is required when ERROR_TRACKING_MODE=sentry");
	}
	const parsed = new URL(normalizedDSN);
	if (parsed.protocol !== "https:" && parsed.hostname !== "localhost") {
		throw new Error("Sentry DSN must use HTTPS outside localhost");
	}
	return { enabled: true, dsn: normalizedDSN };
}
