import { isFixtureIdentityMode } from "./auth-session";

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
// otherwise strand the required terms banner and the /legal routes. It applies
// only when no real identity provider is configured; a configured provider
// still requires real operator content, keeping production behaviour intact.
const devLegalContent: LegalContentConfig = {
	entityName: "Local Dev (placeholder)",
	contactEmail: "dev@localhost",
	termsContent:
		"Placeholder Terms of Service for local development only — not legally binding. Set NEXT_PUBLIC_LEGAL_* before production.",
	privacyContent:
		"Placeholder Privacy Policy for local development only — not legally binding. Set NEXT_PUBLIC_LEGAL_* before production.",
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

// In fixture/dev mode, fill each missing legal field with a placeholder so the
// terms banner and /legal routes work locally; a real identity provider
// (production) still gets operator content verbatim, unpadded, so the gate stays
// enforced until real legal content is supplied.
export const legalContentConfig: LegalContentConfig = isFixtureIdentityMode()
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
