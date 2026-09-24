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

/**
 * Name of the `<meta>` the root layout renders with the deployment's browser
 * error-tracking configuration. instrumentation-client.ts runs before React, so
 * it cannot be handed a prop; it reads this instead — the value the server read
 * from the `error-tracking` group for this request, not one inlined at build.
 */
export const ERROR_TRACKING_META_NAME = "codefly-error-tracking";

export interface BrowserErrorTrackingConfig {
	mode?: string;
	dsn?: string;
	environment?: string;
	sendDefaultPii?: boolean;
}

/** The configuration the root layout published, or none (tracking off). */
export function readBrowserErrorTrackingConfig(
	doc: Pick<Document, "querySelector">,
): BrowserErrorTrackingConfig {
	const content = doc
		.querySelector(`meta[name="${ERROR_TRACKING_META_NAME}"]`)
		?.getAttribute("content");
	if (!content) return {};
	try {
		const parsed = JSON.parse(content) as unknown;
		return typeof parsed === "object" && parsed !== null
			? (parsed as BrowserErrorTrackingConfig)
			: {};
	} catch {
		return {};
	}
}
