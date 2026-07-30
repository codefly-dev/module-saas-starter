export type AbuseProtectionConfiguration =
	| { enabled: false }
	| { enabled: true; siteKey: string };

export function configuredAbuseProtection(
	mode: string | undefined,
	siteKey: string | undefined,
): AbuseProtectionConfiguration {
	const normalized = (mode ?? "disabled").trim().toLowerCase();
	if (normalized === "disabled") {
		if (siteKey?.trim()) {
			throw new Error(
				"Turnstile site key is present while abuse protection is disabled",
			);
		}
		return { enabled: false };
	}
	if (normalized !== "turnstile") {
		throw new Error(
			"NEXT_PUBLIC_ABUSE_PROTECTION_MODE must be disabled or turnstile",
		);
	}
	if (!siteKey?.trim()) {
		throw new Error(
			"Turnstile site key is required when abuse protection is enabled",
		);
	}
	return { enabled: true, siteKey: siteKey.trim() };
}
