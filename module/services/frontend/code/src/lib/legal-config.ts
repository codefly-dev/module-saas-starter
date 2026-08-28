export interface LegalContentConfig {
	entityName?: string;
	contactEmail?: string;
	termsContent?: string;
	privacyContent?: string;
}

export function legalContentConfigured(
	config: LegalContentConfig = legalContentConfig,
): boolean {
	return Boolean(
		config.entityName?.trim() &&
			config.contactEmail?.trim() &&
			config.termsContent?.trim() &&
			config.privacyContent?.trim(),
	);
}

const operatorLegalContent: LegalContentConfig = {
	entityName: process.env.NEXT_PUBLIC_LEGAL_ENTITY_NAME,
	contactEmail: process.env.NEXT_PUBLIC_LEGAL_CONTACT_EMAIL,
	termsContent: process.env.NEXT_PUBLIC_LEGAL_TERMS_CONTENT,
	privacyContent: process.env.NEXT_PUBLIC_LEGAL_PRIVACY_CONTENT,
};

// Non-production dev placeholder. The terms gate requires operator-supplied
// legal content, so the default fixture/dev stack — which ships none — would
// otherwise strand the required terms banner and the /legal routes.
const devLegalContent: LegalContentConfig = {
	entityName: "Local Dev (placeholder)",
	contactEmail: "dev@localhost",
	termsContent:
		"Placeholder Terms of Service for local development only — not legally binding. Set the `legal` Codefly configuration group before production.",
	privacyContent:
		"Placeholder Privacy Policy for local development only — not legally binding. Set the `legal` Codefly configuration group before production.",
};

// Keep an operator-supplied value verbatim when present; otherwise fall back to
// the dev placeholder. Per field, so a fixture stack that sets only some of the
// NEXT_PUBLIC_LEGAL_* vars keeps the real ones instead of discarding them all.
function withDevFallback(
	operator?: string,
	placeholder?: string,
): string | undefined {
	return operator?.trim() ? operator : placeholder;
}

// Dev placeholders require an EXPLICIT affirmative signal, not the mere absence
// of a real identity provider. NEXT_PUBLIC_LEGAL_DEV_PLACEHOLDER is inlined at
// build time from the Codefly fixture boundary (see next.config.mjs), so the
// local fixture stack gets placeholders with zero config while a real deploy —
// which never builds under a fixture — defaults to the closed gate rather than
// silently shipping placeholder terms.
function devPlaceholdersEnabled(): boolean {
	const flag =
		process.env.NEXT_PUBLIC_LEGAL_DEV_PLACEHOLDER?.trim().toLowerCase();
	return flag === "true" || flag === "1";
}

// With placeholders enabled, fill each missing legal field so the terms banner
// and /legal routes work locally; operator-supplied fields are still kept
// verbatim. Otherwise operator content is used unpadded, so the gate stays
// enforced until real legal content is supplied.
export const legalContentConfig: LegalContentConfig = devPlaceholdersEnabled()
	? {
			entityName: withDevFallback(
				operatorLegalContent.entityName,
				devLegalContent.entityName,
			),
			contactEmail: withDevFallback(
				operatorLegalContent.contactEmail,
				devLegalContent.contactEmail,
			),
			termsContent: withDevFallback(
				operatorLegalContent.termsContent,
				devLegalContent.termsContent,
			),
			privacyContent: withDevFallback(
				operatorLegalContent.privacyContent,
				devLegalContent.privacyContent,
			),
		}
	: operatorLegalContent;
