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

// A real identity provider (production) is anything other than unset/fixture/dev.
function fixtureIdentityMode(): boolean {
	const id = process.env.NEXT_PUBLIC_IDENTITY_PROVIDER?.trim().toLowerCase();
	return !id || id === "fixture" || id === "dev";
}

export const legalContentConfig: LegalContentConfig =
	legalContentConfigured(operatorLegalContent) || !fixtureIdentityMode()
		? operatorLegalContent
		: devLegalContent;
